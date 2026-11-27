//go:build unix

package attach

// The attach routine against a real api.Server from the pinned server
// module (api/apitest): the takeover, heartbeat and release are the
// server's own. The attach command is a fake: a script called ssh, first
// on PATH, which the server's ssh argv then runs.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"

	"github.com/Loomux/tui/client"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// world is a test server with a task on a remote target called box, a
// terminal's client of it, and a directory of fake programs that is the
// whole PATH.
type world struct {
	t    *testing.T
	srv  *apitest.Server
	api  *client.Client
	task *registry.Task
	bin  string
	// front, when set, decides the fate of a request before the server
	// sees it: a status to answer with, or 0 to let it through.
	front atomic.Pointer[func(*http.Request) int]
}

func newWorld(t *testing.T, opts ...apitest.Option) *world {
	t.Helper()
	w := &world{t: t, srv: apitest.New(t, opts...), bin: t.TempDir()}
	ws := w.srv.SeedWorkspace("web", w.srv.SeedTarget("box"))
	w.task = w.srv.SeedTask(apitest.Task{WorkspaceID: ws.ID})
	front := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if f := w.front.Load(); f != nil {
			if status := (*f)(r); status != 0 {
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(status)
				_, _ = rw.Write([]byte(`{"error":"the front answered","code":"` + client.DefaultCode(status) + `"}`))
				return
			}
		}
		w.srv.API.ServeHTTP(rw, r)
	}))
	t.Cleanup(front.Close)
	w.api = w.client("laptop", front.URL)
	t.Setenv("PATH", w.bin)
	return w
}

// client is another terminal's session.
func (w *world) client(label, url string) *client.Client {
	w.t.Helper()
	if url == "" {
		url = w.srv.URL
	}
	c, err := client.New(url, w.srv.SeedLinkedSession(label, registry.SessionKindTUI))
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

// answer makes the front answer matching requests with status until the
// returned func is called.
func (w *world) answer(status int, match string) (restore func()) {
	f := func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, match) {
			return status
		}
		return 0
	}
	w.front.Store(&f)
	return func() { w.front.Store(nil) }
}

// program puts a shell script called name on PATH and returns it as a
// command.
func (w *world) program(name, body string) Command {
	w.t.Helper()
	path := filepath.Join(w.bin, name)
	script := "#!/bin/sh\nPATH=/usr/bin:/bin\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		w.t.Fatal(err)
	}
	return Command{Via: name, Argv: []string{name}, Path: path}
}

// file is a path for a script to leave a mark at.
func (w *world) file(name string) string { return filepath.Join(w.bin, name) }

func (w *world) status() registry.TaskStatus { return w.srv.Task(w.task.ID).Status }

// endedReason is why the hold ended, as its holder's heartbeat learns.
func (w *world) endedReason(holdID string) string {
	w.t.Helper()
	_, err := w.api.Heartbeat(ctx(w.t), w.task.ID, holdID)
	var e *client.Error
	if !errors.As(err, &e) || e.Code != client.CodeHoldEnded {
		w.t.Fatalf("heartbeat of hold %s: %v, want hold_ended", holdID, err)
	}
	return e.EndedReason
}

// fast makes a session's timers a test's: a heartbeat every 20 ms.
func fast(s *Session) {
	s.every = 20 * time.Millisecond
	s.retry = 10 * time.Millisecond
	s.beatTimeout = 5 * time.Second
	s.killWait = 2 * time.Second
	s.releaseWait = 5 * time.Millisecond
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func exists(path string) func() bool {
	return func() bool {
		_, err := os.Stat(path)
		return err == nil
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// syncBuffer is a bytes.Buffer two goroutines may use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// Takeover, the server's argv run as it is, release.
func TestTakeRunRelease(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", `printf '%s\n' "$@" > `+w.file("args")+`; echo from the pane`)

	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	if s.TaskID != w.task.ID || s.Hold.ID == "" || s.Hold.Mode != client.ModeDrive || s.Info.Target.Name != "box" {
		t.Errorf("session = %+v", s)
	}
	if h := w.srv.ActiveHold(w.task.ID); h == nil || h.ID != s.Hold.ID {
		t.Fatalf("the server's hold = %+v, want %s", h, s.Hold.ID)
	}
	if st := w.status(); st != registry.TaskStatusHumanTakeover {
		t.Errorf("status while held = %s, want human-takeover", st)
	}
	if len(s.Plan.Commands) != 1 || s.Plan.Commands[0].Via != "ssh" {
		t.Fatalf("plan = %+v, want the server's ssh command", s.Plan)
	}

	var out, errOut syncBuffer
	res, err := s.Run(ctx(t), Terminal{Out: &out, Err: &errOut})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, errOut.String())
	}
	if !res.Attached || len(res.Attempts) != 1 || res.Status != client.TaskAwaitingInput {
		t.Errorf("result = %+v", res)
	}
	// The command ran with exactly the server's arguments, the remote
	// command as one of them: no shell split it.
	want := "loomux@box.test\n-t\ntmux -L " + apitest.TmuxSocket + " attach -t loomux-" + w.task.ID + "\n"
	if got := readFile(t, w.file("args")); got != want {
		t.Errorf("ssh's arguments = %q, want %q", got, want)
	}
	if out.String() != "from the pane\n" {
		t.Errorf("the command's output = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "attaching to box via ssh") || !strings.Contains(errOut.String(), "C-b d") {
		t.Errorf("no line saying where it attaches and how to detach in %q", errOut.String())
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held after Run: %+v", h)
	}
	if st := w.status(); st != registry.TaskStatusAwaitingInput {
		t.Errorf("status after Run = %s, want awaiting-input", st)
	}
	if r := w.endedReason(s.Hold.ID); r != "released" {
		t.Errorf("the hold ended %q, want released", r)
	}
}

func TestTakeUnknownTask(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	if _, err := Take(ctx(t), w.api, "no-such-task", Options{}); !client.Is(err, client.CodeNotFound) {
		t.Errorf("err = %v, want not_found", err)
	}
}

// With no command this machine can run, nothing is taken: a running
// turn isn't ended for an attach that can't happen.
func TestTakeWithNothingToRunTakesNoHold(t *testing.T) {
	w := newWorld(t) // no ssh on PATH
	running := w.srv.SeedTask(apitest.Task{WorkspaceID: w.task.WorkspaceID, Status: registry.TaskStatusRunning})
	_, err := Take(ctx(t), w.api, running.ID, Options{})
	var none *NoCommandError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want a NoCommandError", err)
	}
	if len(none.Skipped) != 1 || none.Skipped[0].Via != "ssh" || !strings.Contains(none.Skipped[0].Line, "ssh loomux@box.test") {
		t.Errorf("skipped = %+v, want the ssh command and why", none.Skipped)
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("the error doesn't say why: %q", err)
	}
	if h := w.srv.ActiveHold(running.ID); h != nil {
		t.Errorf("a hold was taken: %+v", h)
	}
	if st := w.srv.Task(running.ID).Status; st != registry.TaskStatusRunning {
		t.Errorf("status = %s, want it left running", st)
	}
}

// A server from before the takeover route answers 404: it can't attach
// yet. The command is given back to show as text, and never run.
func TestTakeFromAServerWithoutTakeover(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "touch "+w.file("ran"))
	w.answer(http.StatusNotFound, "/takeover")
	_, err := Take(ctx(t), w.api, w.task.ID, Options{})
	var old *UnsupportedError
	if !errors.As(err, &old) {
		t.Fatalf("err = %v, want an UnsupportedError", err)
	}
	if len(old.Plan.Commands) != 1 || !strings.HasPrefix(old.Plan.Commands[0].String(), "ssh loomux@box.test -t 'tmux") {
		t.Errorf("plan = %+v, want the ssh command to show", old.Plan)
	}
	if !strings.Contains(err.Error(), "upgrade") {
		t.Errorf("the error doesn't say to upgrade the server: %q", err)
	}
	if exists(w.file("ran"))() {
		t.Error("the command ran without a hold")
	}
}

func TestTakeHeldTask(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	desk := w.client("desk", "")
	theirs, err := desk.Takeover(ctx(t), w.task.ID, client.TakeoverRequest{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = Take(ctx(t), w.api, w.task.ID, Options{})
	var e *client.Error
	if !errors.As(err, &e) || e.Code != client.CodeTaskHeld || e.Hold == nil || e.Hold.ClientLabel != "desk" {
		t.Fatalf("err = %#v, want task_held with desk's hold", err)
	}
	if h := w.srv.ActiveHold(w.task.ID); h == nil || h.ID != theirs.Hold.ID {
		t.Errorf("the hold = %+v, want desk's still", h)
	}

	s, err := take(ctx(t), w.api, w.task.ID, Options{Force: true}, fast)
	if err != nil {
		t.Fatal(err)
	}
	if h := w.srv.ActiveHold(w.task.ID); h == nil || h.ID != s.Hold.ID || h.ClientLabel != "laptop" {
		t.Errorf("the hold = %+v, want the laptop's", h)
	}
	if err := s.Release(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held after Release: %+v", h)
	}
	// Release ends the heartbeat too, and may be called again.
	if err := s.Release(ctx(t)); err != nil {
		t.Errorf("a second Release: %v", err)
	}
}

func TestTakeInactiveTask(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	done := w.srv.SeedTask(apitest.Task{WorkspaceID: w.task.WorkspaceID, Status: registry.TaskStatusCompleted})
	if _, err := Take(ctx(t), w.api, done.ID, Options{}); !client.Is(err, client.CodeTaskInactive) {
		t.Errorf("err = %v, want task_inactive", err)
	}
}

// A command that fails fast (non-zero within the window) is followed by
// an offer of the next.
func TestRunOffersTheNextCommandAfterAFastFailure(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	first := w.program("kubectl", "echo no rights >&2; exit 1")
	second := w.program("docker", "touch "+w.file("second-ran"))
	s.Plan.Commands = []Command{first, second}

	var offered []string
	var errOut syncBuffer
	res, err := s.Run(ctx(t), Terminal{Err: &errOut, Offer: func(_ context.Context, failed Attempt, next Command) bool {
		offered = append(offered, failed.Command.Via+"→"+next.Via)
		if failed.Err == nil {
			t.Error("the failed attempt has no error")
		}
		// The hold is kept while the person decides.
		if h := w.srv.ActiveHold(w.task.ID); h == nil {
			t.Error("the hold was given up before the next command was offered")
		}
		return true
	}})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, errOut.String())
	}
	if !slices.Equal(offered, []string{"kubectl→docker"}) {
		t.Errorf("offers = %v", offered)
	}
	if !res.Attached || len(res.Attempts) != 2 || !exists(w.file("second-ran"))() {
		t.Errorf("result = %+v, want the second command to have attached", res)
	}
	if !strings.Contains(errOut.String(), "kubectl") || !strings.Contains(errOut.String(), "exit status 1") {
		t.Errorf("the failure isn't reported: %q", errOut.String())
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
}

func TestRunReleasesWhenTheNextCommandIsDeclined(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	s.Plan.Commands = []Command{w.program("kubectl", "exit 1"), w.program("docker", "touch "+w.file("second-ran"))}
	res, err := s.Run(ctx(t), Terminal{Offer: func(context.Context, Attempt, Command) bool { return false }})
	var failed *FailedError
	if !errors.As(err, &failed) || len(failed.Attempts) != 1 {
		t.Fatalf("err = %v, want a FailedError with the one attempt", err)
	}
	if res.Attached || exists(w.file("second-ran"))() {
		t.Errorf("result = %+v: the declined command ran", res)
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
	if st := w.status(); st != registry.TaskStatusAwaitingInput {
		t.Errorf("status = %s, want it handed back", st)
	}
}

// Every command failing ends in a release and an error that names each.
func TestRunAllCommandsFail(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	gone := Command{Via: "gone", Argv: []string{"gone"}, Path: w.file("gone")} // was on PATH when chosen, isn't now
	s.Plan.Commands = []Command{gone, w.program("kubectl", "exit 7")}
	_, err = s.Run(ctx(t), Terminal{}) // no Offer: the next is tried
	var failed *FailedError
	if !errors.As(err, &failed) || len(failed.Attempts) != 2 {
		t.Fatalf("err = %v, want a FailedError with both attempts", err)
	}
	if !strings.Contains(err.Error(), "gone") || !strings.Contains(err.Error(), "exit status 7") {
		t.Errorf("the error doesn't name both failures: %q", err)
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
}

// ssh's 255 is its own failure whenever it comes; any other status after
// the window is just how the session ended.
func TestRunFailFastWindow(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	short := func(s *Session) {
		fast(s)
		s.failFast = 20 * time.Millisecond
	}

	s, err := take(ctx(t), w.api, w.task.ID, Options{}, short)
	if err != nil {
		t.Fatal(err)
	}
	s.Plan.Commands = []Command{w.program("ssh", "sleep 0.3; exit 255"), w.program("docker", "exit 0")}
	res, err := s.Run(ctx(t), Terminal{})
	if err != nil || len(res.Attempts) != 2 {
		t.Errorf("ssh's 255 after the window: %+v, %v; want the next command tried", res, err)
	}

	// With no other way in, an ssh that was up for a while and then lost
	// its connection is how the session ended, not a failure to attach.
	s, err = take(ctx(t), w.api, w.task.ID, Options{}, short)
	if err != nil {
		t.Fatal(err)
	}
	s.Plan.Commands = []Command{w.program("ssh", "sleep 0.3; exit 255")}
	res, err = s.Run(ctx(t), Terminal{})
	if err != nil || !res.Attached {
		t.Errorf("ssh's 255 after the window, with nothing else to try: %+v, %v; want it attached and no error", res, err)
	}
	// Within the window it is a failure like any other.
	s, err = take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	s.Plan.Commands = []Command{w.program("ssh", "exit 255")}
	var failed *FailedError
	if res, err = s.Run(ctx(t), Terminal{}); !errors.As(err, &failed) || res.Attached {
		t.Errorf("ssh's 255 at once: %+v, %v; want a FailedError", res, err)
	}

	s, err = take(ctx(t), w.api, w.task.ID, Options{}, short)
	if err != nil {
		t.Fatal(err)
	}
	s.Plan.Commands = []Command{w.program("kubectl", "sleep 0.3; exit 3"), w.program("docker", "touch "+w.file("second-ran"))}
	var errOut syncBuffer
	res, err = s.Run(ctx(t), Terminal{Err: &errOut})
	if err != nil || len(res.Attempts) != 1 || !res.Attached || exists(w.file("second-ran"))() {
		t.Errorf("a late non-zero exit: %+v, %v; want it to be the end of the session", res, err)
	}
	if !strings.Contains(errOut.String(), "exit status 3") {
		t.Errorf("the exit status isn't mentioned: %q", errOut.String())
	}
}

// countingAPI counts the heartbeats that reach the server.
type countingAPI struct {
	API
	beats atomic.Int32
}

func (c *countingAPI) Heartbeat(ctx context.Context, taskID, holdID string) (*client.Hold, error) {
	c.beats.Add(1)
	return c.API.Heartbeat(ctx, taskID, holdID)
}

// The heartbeat runs from the takeover on, not only while a command
// runs, and stops with the release.
func TestHeartbeatRenewsTheHold(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exec sleep 1")
	api := &countingAPI{API: w.api}
	s, err := take(ctx(t), api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "heartbeats before Run", func() bool { return api.beats.Load() >= 2 })
	if h := w.srv.ActiveHold(w.task.ID); h == nil || !h.RenewedAt.After(h.AcquiredAt) {
		t.Errorf("the server's hold wasn't renewed: %+v", h)
	}
	before := api.beats.Load()
	if _, err := s.Run(ctx(t), Terminal{}); err != nil {
		t.Fatal(err)
	}
	// A heartbeat is sent every 20 ms but only after the one before it is
	// answered, so even an unloaded machine gets fewer than 50 in a
	// second; under load it gets far fewer. Five in a second is a
	// renewal still running, not a count of the timer.
	if got := api.beats.Load() - before; got < 5 {
		t.Errorf("%d heartbeats during a 1 s attach at one per 20 ms", got)
	}
	after := api.beats.Load()
	time.Sleep(100 * time.Millisecond)
	if api.beats.Load() != after {
		t.Error("heartbeats went on after the release")
	}
}

// Taken back from another client: the heartbeat learns the hold is gone,
// and this terminal's attach is ended, whether or not the server's detach
// reached it.
func TestRunEndsTheCommandWhenTheHoldIsTakenBack(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "touch "+w.file("started")+"; exec sleep 60")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	phone := w.client("phone", "")
	takenBack := make(chan struct{})
	go func() {
		defer close(takenBack)
		waitFor(t, "the command to start", exists(w.file("started")))
		if _, err := phone.Release(ctx(t), w.task.ID, ""); err != nil {
			t.Errorf("take back: %v", err)
		}
	}()
	start := time.Now()
	res, err := s.Run(ctx(t), Terminal{})
	<-takenBack
	var ended *EndedError
	if !errors.As(err, &ended) || ended.Reason != "taken_back" || ended.At.IsZero() {
		t.Fatalf("err = %#v, want an EndedError for taken_back with its time", err)
	}
	if !strings.Contains(err.Error(), "taken back") {
		t.Errorf("message = %q", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Errorf("Run took %s: the command wasn't ended", time.Since(start))
	}
	if !res.Attached {
		t.Errorf("result = %+v, want it to count as attached", res)
	}
}

// Signed out elsewhere: the heartbeat's 401 ends the attach.
func TestRunEndsTheCommandWhenSignedOut(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "touch "+w.file("started")+"; exec sleep 60")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	loggedOut := make(chan struct{})
	go func() {
		defer close(loggedOut)
		waitFor(t, "the command to start", exists(w.file("started")))
		if err := w.api.Logout(ctx(t)); err != nil {
			t.Errorf("logout: %v", err)
		}
	}()
	_, err = s.Run(ctx(t), Terminal{})
	<-loggedOut
	if !client.Unauthorized(err) && !isEnded(err, "session_revoked") {
		t.Fatalf("err = %#v, want the 401 or the hold ended as session_revoked", err)
	}
	if !strings.Contains(err.Error(), "loomux login") {
		t.Errorf("the message doesn't say to sign in again: %q", err)
	}
}

func isEnded(err error, reason string) bool {
	var ended *EndedError
	return errors.As(err, &ended) && ended.Reason == reason
}

// detachHeartbeat401 is an API whose heartbeat waits for the release to
// cancel it and then answers 401, while the release itself fails for
// another reason.
type detachHeartbeat401 struct {
	API
	fail     atomic.Bool
	inFlight chan struct{}
	once     sync.Once
}

func (a *detachHeartbeat401) Heartbeat(ctx context.Context, taskID, holdID string) (*client.Hold, error) {
	if !a.fail.Load() {
		return a.API.Heartbeat(ctx, taskID, holdID)
	}
	a.once.Do(func() { close(a.inFlight) })
	<-ctx.Done()
	return nil, &client.Error{Status: http.StatusUnauthorized, Code: client.CodeUnauthorized, Message: "unauthorized"}
}

func (a *detachHeartbeat401) Release(context.Context, string, string) (*client.Release, error) {
	return nil, errors.New("gateway down")
}

// A heartbeat 401 that arrives as the release cancels the heartbeat isn't
// lost: the outcome carries both the 401 and the failed release.
func TestReleaseKeepsAHeartbeat401SeenWhileDetaching(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exec sleep 60")
	api := &detachHeartbeat401{API: w.api, inFlight: make(chan struct{})}
	s, err := take(ctx(t), api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	api.fail.Store(true)
	select {
	case <-api.inFlight:
	case <-time.After(10 * time.Second):
		t.Fatal("no heartbeat in flight")
	}
	err = s.Release(ctx(t))
	if !client.Unauthorized(err) {
		t.Errorf("Release = %v, want the heartbeat's 401", err)
	}
	var rel *ReleaseError
	if !errors.As(err, &rel) {
		t.Errorf("Release = %v, want the failed release too", err)
	}
}

// While the API doesn't answer (a deploy), the attach stays up and the
// heartbeat keeps trying; it resumes when the API is back.
func TestHeartbeatSurvivesAnOutage(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "touch "+w.file("started")+"; while [ ! -e "+w.file("detach")+" ]; do sleep 0.02; done")
	api := &countingAPI{API: w.api}
	s, err := take(ctx(t), api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Run(ctx(t), Terminal{})
		done <- err
	}()
	waitFor(t, "the command to start", exists(w.file("started")))

	restore := w.answer(http.StatusServiceUnavailable, "/takeover/heartbeat")
	n := api.beats.Load()
	waitFor(t, "heartbeats tried during the outage", func() bool { return api.beats.Load() >= n+5 })
	select {
	case err := <-done:
		t.Fatalf("Run ended during the outage: %v", err)
	default:
	}
	restore()
	renewed := w.srv.ActiveHold(w.task.ID).RenewedAt
	waitFor(t, "a renewal after the outage", func() bool {
		h := w.srv.ActiveHold(w.task.ID)
		return h != nil && h.RenewedAt.After(renewed)
	})

	if err := os.WriteFile(w.file("detach"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("Run: %v", err)
	}
}

// The server ended the hold (the lease ran out) and the attach ended
// before a heartbeat said so: the release's answer carries the reason.
func TestRunReportsAHoldThatEndedMeanwhile(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	w := newWorld(t, apitest.WithClock(clock))
	w.program("ssh", "touch "+w.file("started")+"; while [ ! -e "+w.file("detach")+" ]; do sleep 0.02; done")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, func(s *Session) {
		fast(s)
		s.every = time.Hour // no heartbeat gets in first
	})
	if err != nil {
		t.Fatal(err)
	}
	swept := make(chan struct{})
	go func() {
		defer close(swept)
		waitFor(t, "the command to start", exists(w.file("started")))
		mu.Lock()
		now = now.Add(10 * time.Minute)
		mu.Unlock()
		if n := w.srv.SweepHolds(); n != 1 {
			t.Errorf("the sweep ended %d holds, want 1", n)
		}
		_ = os.WriteFile(w.file("detach"), nil, 0o644)
	}()
	_, err = s.Run(ctx(t), Terminal{})
	<-swept
	if !isEnded(err, "expired") {
		t.Fatalf("err = %#v, want an EndedError for expired", err)
	}
	if !strings.Contains(err.Error(), "your hold expired at ") || !strings.Contains(err.Error(), "the session was detached") {
		t.Errorf("message = %q", err)
	}
}

// Cancelled (a signal): the command is ended and the hold released.
func TestRunCancelled(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "touch "+w.file("started")+"; exec sleep 60")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(ctx(t))
	go func() {
		waitFor(t, "the command to start", exists(w.file("started")))
		cancel()
	}()
	start := time.Now()
	_, err = s.Run(c, Terminal{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Errorf("Run took %s: the command wasn't ended", time.Since(start))
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held after a cancelled Run: %+v", h)
	}
	if r := w.endedReason(s.Hold.ID); r != "released" {
		t.Errorf("the hold ended %q, want released", r)
	}
}

// Cancelled before the command started (a signal while the takeover was
// answered): nothing is run, and the hold is released.
func TestRunAlreadyCancelled(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "touch "+w.file("ran"))
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(ctx(t))
	cancel()
	res, err := s.Run(c, Terminal{})
	if !errors.Is(err, context.Canceled) || len(res.Attempts) != 0 || res.Attached {
		t.Errorf("Run = %+v, %v; want nothing run and context.Canceled", res, err)
	}
	time.Sleep(50 * time.Millisecond)
	if exists(w.file("ran"))() {
		t.Error("the command ran after the cancel")
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
}

// Two terminals of one session share its hold (a takeover by the session
// that holds the task gets that hold back). When one detaches and
// releases it, the other's attach is ended too: nobody stays in a pane
// that automation may type into again.
func TestRunEndsWhenTheSessionsHoldIsReleasedElsewhere(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "touch "+w.file("started")+"; exec sleep 60")
	first, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	second, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	if second.Hold.ID != first.Hold.ID {
		t.Fatalf("the session's second takeover got hold %s, want its own %s back", second.Hold.ID, first.Hold.ID)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		waitFor(t, "the command to start", exists(w.file("started")))
		if err := first.Release(ctx(t)); err != nil {
			t.Errorf("the first terminal's release: %v", err)
		}
	}()
	_, err = second.Run(ctx(t), Terminal{})
	<-released
	if !isEnded(err, "released") {
		t.Fatalf("err = %#v, want an EndedError for released", err)
	}
	if !strings.Contains(err.Error(), "another terminal") {
		t.Errorf("message = %q", err)
	}
}

// A session is one attach: once its hold is released, Run runs nothing.
// A command never runs without a hold.
func TestRunAfterTheReleaseRunsNothing(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "echo ran >> "+w.file("runs"))
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx(t), Terminal{}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Run(ctx(t), Terminal{})
	if err == nil || len(res.Attempts) != 0 {
		t.Errorf("a second Run = %+v, %v; want an error and nothing run", res, err)
	}

	s, err = take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx(t)); err != nil {
		t.Fatal(err)
	}
	res, err = s.Run(ctx(t), Terminal{})
	if err == nil || len(res.Attempts) != 0 {
		t.Errorf("Run after Release = %+v, %v; want an error and nothing run", res, err)
	}
	if got := readFile(t, w.file("runs")); got != "ran\n" {
		t.Errorf("the command ran %q, want once", got)
	}
}

// Released while a command has the terminal: the command is ended.
func TestReleaseDuringRunEndsTheCommand(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "touch "+w.file("started")+"; exec sleep 60")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		waitFor(t, "the command to start", exists(w.file("started")))
		if err := s.Release(ctx(t)); err != nil {
			t.Errorf("Release: %v", err)
		}
	}()
	start := time.Now()
	res, err := s.Run(ctx(t), Terminal{})
	<-released
	if err != nil || !res.Attached {
		t.Errorf("Run = %+v, %v", res, err)
	}
	if time.Since(start) > 20*time.Second {
		t.Errorf("Run took %s: the command wasn't ended", time.Since(start))
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
}

// releaseWhen notes, when the hold is released, whether a file was there.
type releaseWhen struct {
	API
	file  string
	there atomic.Bool
}

func (a *releaseWhen) Release(ctx context.Context, taskID, holdID string) (*client.Release, error) {
	a.there.Store(exists(a.file)())
	return a.API.Release(ctx, taskID, holdID)
}

// A Release during Run doesn't reach the server before the command is
// gone: the server may type into the pane the moment the hold is
// released, and the person must be out of it by then.
func TestReleaseDuringRunWaitsForTheCommand(t *testing.T) {
	w := newWorld(t)
	// A command that takes a while to let go after its hangup.
	w.program("ssh", "trap 'sleep 0.3; touch "+w.file("ended")+"; exit 0' HUP; touch "+w.file("started")+"; while :; do sleep 0.02; done")
	api := &releaseWhen{API: w.api, file: w.file("ended")}
	s, err := take(ctx(t), api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		waitFor(t, "the command to start", exists(w.file("started")))
		if err := s.Release(ctx(t)); err != nil {
			t.Errorf("Release: %v", err)
		}
	}()
	if _, err := s.Run(ctx(t), Terminal{}); err != nil {
		t.Errorf("Run: %v", err)
	}
	<-released
	if !api.there.Load() {
		t.Error("the hold was released while the command was still on the terminal")
	}
}

// A question about the next command ends, as a no, when the hold is
// released meanwhile.
func TestReleaseDuringTheOfferEndsIt(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	s.Plan.Commands = []Command{w.program("kubectl", "exit 1"), w.program("docker", "touch "+w.file("second-ran"))}
	asked := make(chan struct{})
	released := make(chan struct{})
	go func() {
		defer close(released)
		<-asked
		if err := s.Release(ctx(t)); err != nil {
			t.Errorf("Release: %v", err)
		}
	}()
	left := make(chan struct{})
	_, err = s.Run(ctx(t), Terminal{Offer: func(c context.Context, _ Attempt, _ Command) bool {
		close(asked)
		<-c.Done() // a person who never answers
		close(left)
		return true
	}})
	// The question was told to end, and had ended when Run returned: it
	// isn't still reading the terminal.
	select {
	case <-left:
	default:
		t.Error("Run returned with the question still up")
	}
	<-released
	var failed *FailedError
	if !errors.As(err, &failed) || exists(w.file("second-ran"))() {
		t.Errorf("err = %v, second ran = %v; want the first failure and nothing more run", err, exists(w.file("second-ran"))())
	}
}

// A command that ignores the hangup is killed.
func TestRunKillsACommandThatWontEnd(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "trap '' HUP; touch "+w.file("started")+"; while :; do sleep 0.05; done")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, func(s *Session) {
		fast(s)
		s.killWait = 100 * time.Millisecond
	})
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(ctx(t))
	go func() {
		waitFor(t, "the command to start", exists(w.file("started")))
		cancel()
	}()
	start := time.Now()
	if _, err = s.Run(c, Terminal{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Errorf("Run took %s: the command wasn't killed", time.Since(start))
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
}

// A release that can't reach the server is tried again, and reported
// with when the hold runs out by itself.
func TestRunReportsAReleaseThatFailed(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	var tries atomic.Int32
	f := func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, "/release") {
			tries.Add(1)
			return http.StatusServiceUnavailable
		}
		return 0
	}
	w.front.Store(&f)
	_, err = s.Run(ctx(t), Terminal{})
	var rel *ReleaseError
	if !errors.As(err, &rel) || rel.Expires.IsZero() {
		t.Fatalf("err = %#v, want a ReleaseError with the lease's end", err)
	}
	if tries.Load() < 2 {
		t.Errorf("the release was tried %d times, want it retried", tries.Load())
	}
	if !strings.Contains(err.Error(), "expires") {
		t.Errorf("message = %q, want it to say the hold expires by itself", err)
	}

	// A release that fails once is retried and succeeds.
	s, err = take(ctx(t), w.api, w.task.ID, Options{}, fast)
	if err != nil {
		t.Fatal(err)
	}
	tries.Store(0)
	once := func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, "/release") && tries.Add(1) == 1 {
			return http.StatusBadGateway
		}
		return 0
	}
	w.front.Store(&once)
	if _, err := s.Run(ctx(t), Terminal{}); err != nil {
		t.Errorf("Run with one failed release: %v", err)
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
}

// A drive is asked for, so anything else the server answers with is
// given back at once and never attached to.
func TestTakeRefusesAHoldThatIsNotADrive(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "touch "+w.file("ran"))
	api := &watchAPI{API: w.api}
	_, err := Take(ctx(t), api, w.task.ID, Options{})
	if err == nil || !strings.Contains(err.Error(), "watch") {
		t.Fatalf("err = %v, want the watch hold refused", err)
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("the hold wasn't given back: %+v", h)
	}

	// It can't be given back: the error says so, and not that it was.
	defer w.answer(http.StatusNotFound, "/release")()
	_, err = Take(ctx(t), api, w.task.ID, Options{})
	var rel *ReleaseError
	if !errors.As(err, &rel) || strings.Contains(err.Error(), "given back") {
		t.Errorf("err = %v, want the failed release and no word of a give-back", err)
	}
	if h := w.srv.ActiveHold(w.task.ID); h == nil {
		t.Error("the release went through: nothing to report")
	}
}

// watchAPI answers a takeover with its hold relabelled as a watch.
type watchAPI struct{ API }

func (a *watchAPI) Takeover(ctx context.Context, taskID string, r client.TakeoverRequest) (*client.Takeover, error) {
	tk, err := a.API.Takeover(ctx, taskID, r)
	if err == nil {
		tk.Hold.Mode = client.ModeWatch
	}
	return tk, err
}

// The local target's tmux is another server than the one the terminal
// may be inside, so its command runs without TMUX; any other command
// keeps the environment.
func TestRunLocalCommandWithoutTMUX(t *testing.T) {
	w := newWorld(t)
	w.program("ssh", "exit 0")
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	for via, want := range map[string]string{ViaLocal: "unset", "ssh": "/tmp/tmux-1000/default,1,0"} {
		s, err := take(ctx(t), w.api, w.task.ID, Options{}, fast)
		if err != nil {
			t.Fatal(err)
		}
		c := w.program("tmux", `printf %s "${TMUX-unset}" > `+w.file("tmux-env"))
		c.Via = via
		s.Plan.Commands = []Command{c}
		var errOut syncBuffer
		if _, err := s.Run(ctx(t), Terminal{Err: &errOut}); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, w.file("tmux-env")); got != want {
			t.Errorf("via %s: the command's TMUX = %q, want %q", via, got, want)
		}
		// Inside tmux, the prefix reaches the inner tmux when pressed twice.
		if !strings.Contains(errOut.String(), "C-b C-b d") {
			t.Errorf("via %s: no word on detaching from inside tmux in %q", via, errOut.String())
		}
	}
}

func TestBeatDelay(t *testing.T) {
	s := &Session{every: 15 * time.Second, retry: 5 * time.Second}
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		failed  bool
		expires time.Time
		want    time.Duration
	}{
		{"after a renewal", false, now.Add(45 * time.Second), 15 * time.Second},
		{"after a failure, the lease still running", true, now.Add(30 * time.Second), 5 * time.Second},
		{"after a failure, the lease about to run out", true, now.Add(2 * time.Second), 5 * time.Second},
		{"after a failure, the lease run out", true, now.Add(-time.Second), 15 * time.Second},
	} {
		if got := s.beatDelay(tc.failed, now, tc.expires); got != tc.want {
			t.Errorf("%s: wait %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestEndedErrorMessages(t *testing.T) {
	at := time.Date(2026, 11, 20, 14, 31, 0, 0, time.Local)
	for reason, want := range map[string]string{
		"expired":         "your hold expired at 14:31; the session was detached",
		"taken_back":      "the task was taken back from another client at 14:31; the session was detached",
		"taken_over":      "another session took the task over at 14:31; the session was detached",
		"session_revoked": "this session was signed out at 14:31; the session was detached. Sign in again with loomux login",
		"task_ended":      "the task ended at 14:31",
		"released":        "this session's hold was released from another terminal at 14:31; the attach was ended",
		"something_new":   "your hold ended at 14:31 (something_new)",
	} {
		if got := (&EndedError{Reason: reason, At: at}).Error(); got != want {
			t.Errorf("%s: %q, want %q", reason, got, want)
		}
	}
	if got := (&EndedError{Reason: "expired"}).Error(); got != "your hold expired; the session was detached" {
		t.Errorf("without a time: %q", got)
	}
}

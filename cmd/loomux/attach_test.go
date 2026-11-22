//go:build unix

package main

// `loomux attach` against a real api.Server from the pinned server module
// (api/apitest). The attach command is a fake: a script called ssh, the
// only program on PATH, which the server's ssh argv then runs.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"

	"github.com/Loomux/tui/auth"
	"github.com/Loomux/tui/client"
)

// attachEnv is a test server with a task on a remote target called box,
// and a signed-in profile for it: a config file and a token file, as
// `loomux login` leaves them.
type attachEnv struct {
	t     *testing.T
	srv   *apitest.Server
	task  *registry.Task
	token string
	bin   string
	// front is the server URL in the config: a handler in front of the
	// server that refuse, when set, may answer first.
	refuse func(*http.Request) int
}

func newAttachEnv(t *testing.T, opts ...apitest.Option) *attachEnv {
	t.Helper()
	e := &attachEnv{t: t, srv: apitest.New(t, opts...), bin: t.TempDir()}
	ws := e.srv.SeedWorkspace("web", e.srv.SeedTarget("box"))
	e.task = e.srv.SeedTask(apitest.Task{WorkspaceID: ws.ID})
	e.token = e.srv.SeedLinkedSession("laptop", registry.SessionKindTUI)

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.refuse != nil {
			if status := e.refuse(r); status != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"not found","code":"not_found"}`))
				return
			}
		}
		e.srv.API.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)

	config, state := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("LOOMUX_PROFILE", "")
	t.Setenv("LOOMUX_SERVER", "")
	t.Setenv("TMUX", "")
	if err := os.MkdirAll(filepath.Join(config, "loomux"), 0o700); err != nil {
		t.Fatal(err)
	}
	toml := "token_store = \"file\"\n\n[profiles.test]\nserver = \"" + front.URL + "\"\n"
	if err := os.WriteFile(filepath.Join(config, "loomux", "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := auth.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.NewFileStore(dir).Set(auth.Account{Profile: "test", Server: front.URL}, e.token); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", e.bin)
	e.terminal(true)
	e.stdin("")
	return e
}

// terminal says whether the standard streams are a terminal.
func (e *attachEnv) terminal(is bool) {
	old := isTerminal
	isTerminal = func() bool { return is }
	e.t.Cleanup(func() { isTerminal = old })
}

// stdin is what the person types at a question.
func (e *attachEnv) stdin(typed string) {
	old := stdin
	stdin = strings.NewReader(typed)
	e.t.Cleanup(func() { stdin = old })
}

// ssh puts the fake ssh on PATH.
func (e *attachEnv) ssh(body string) {
	e.t.Helper()
	script := "#!/bin/sh\nPATH=/usr/bin:/bin\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(e.bin, "ssh"), []byte(script), 0o755); err != nil {
		e.t.Fatal(err)
	}
}

func (e *attachEnv) file(name string) string { return filepath.Join(e.bin, name) }

func (e *attachEnv) ran(name string) bool {
	_, err := os.Stat(e.file(name))
	return err == nil
}

// other is another terminal's client.
func (e *attachEnv) other(label string) *client.Client {
	e.t.Helper()
	c, err := client.New(e.srv.URL, e.srv.SeedLinkedSession(label, registry.SessionKindTUI))
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func attachCtx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

// loomux runs one invocation.
func loomux(args ...string) (code int, stdout, stderr string) {
	var out, errOut lockedBuffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestAttachTask(t *testing.T) {
	e := newAttachEnv(t)
	e.ssh(`printf '%s\n' "$@" > ` + e.file("args") + `; echo in the pane`)
	code, stdout, stderr := loomux("attach", "--task", e.task.ID)
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	args, err := os.ReadFile(e.file("args"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "loomux@box.test\n-t\ntmux -L " + apitest.TmuxSocket + " attach -t loomux-" + e.task.ID + "\n"; string(args) != want {
		t.Errorf("ssh's arguments = %q, want %q", args, want)
	}
	if stdout != "in the pane\n" {
		t.Errorf("stdout = %q, want the command's output alone", stdout)
	}
	for _, want := range []string{"attaching to box via ssh", "released", "awaiting_input"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr has no %q:\n%s", want, stderr)
		}
	}
	if h := e.srv.ActiveHold(e.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusAwaitingInput {
		t.Errorf("status = %s, want awaiting-input", st)
	}
}

// `loomux attach <conversation>` attaches to the conversation's open
// agent task; an id that isn't a conversation is a task's.
func TestAttachConversationOrTask(t *testing.T) {
	e := newAttachEnv(t)
	conv := e.task.ConversationID
	e.srv.SeedTask(apitest.Task{ConversationID: conv, WorkspaceID: e.task.WorkspaceID, Status: registry.TaskStatusCompleted})
	for _, id := range []string{conv, e.task.ID} {
		e.ssh(`printf '%s\n' "$@" > ` + e.file("args"))
		os.Remove(e.file("args"))
		code, _, stderr := loomux("attach", id)
		if code != 0 {
			t.Fatalf("attach %s: exit %d, stderr:\n%s", id, code, stderr)
		}
		args, _ := os.ReadFile(e.file("args"))
		if !strings.Contains(string(args), "attach -t loomux-"+e.task.ID+"\n") {
			t.Errorf("attach %s ran ssh with %q, want the open task's session", id, args)
		}
	}

	// A conversation with nothing to attach to says so, and takes nothing.
	done := e.srv.SeedTask(apitest.Task{WorkspaceID: e.task.WorkspaceID, Status: registry.TaskStatusCompleted})
	code, _, stderr := loomux("attach", done.ConversationID)
	if code != 1 || !strings.Contains(stderr, "no agent to attach to") {
		t.Errorf("a finished conversation: exit %d, stderr %q", code, stderr)
	}
	code, _, stderr = loomux("attach", "no-such-id")
	if code != 1 || !strings.Contains(stderr, "no such task") {
		t.Errorf("an unknown id: exit %d, stderr %q", code, stderr)
	}
}

// Another session holds the task: it is named, and taken from only on a
// yes (or --force).
func TestAttachHeldTask(t *testing.T) {
	e := newAttachEnv(t)
	e.ssh("touch " + e.file("ran"))
	desk := e.other("desk")
	theirs, err := desk.Takeover(attachCtx(t), e.task.ID, client.TakeoverRequest{})
	if err != nil {
		t.Fatal(err)
	}

	for _, typed := range []string{"n\n", "\n", ""} {
		e.stdin(typed)
		code, _, stderr := loomux("attach", "--task", e.task.ID)
		if code != 1 {
			t.Errorf("answer %q: exit %d, want 1", typed, code)
		}
		if !strings.Contains(stderr, "desk") || !strings.Contains(stderr, "[y/N]") {
			t.Errorf("answer %q: stderr doesn't name the holder and ask:\n%s", typed, stderr)
		}
		if h := e.srv.ActiveHold(e.task.ID); h == nil || h.ID != theirs.Hold.ID {
			t.Fatalf("answer %q: the hold = %+v, want desk's still", typed, h)
		}
	}
	if e.ran("ran") {
		t.Fatal("the command ran without the task being taken")
	}

	e.stdin("y\n")
	if code, _, stderr := loomux("attach", "--task", e.task.ID); code != 0 {
		t.Fatalf("answer y: exit %d, stderr:\n%s", code, stderr)
	}
	if !e.ran("ran") {
		t.Error("the command didn't run after a yes")
	}
	if _, err := desk.Heartbeat(attachCtx(t), e.task.ID, theirs.Hold.ID); !client.Is(err, client.CodeHoldEnded) {
		t.Errorf("desk's hold after the takeover: %v, want hold_ended", err)
	}

	// --force asks nothing.
	if _, err := desk.Takeover(attachCtx(t), e.task.ID, client.TakeoverRequest{}); err != nil {
		t.Fatal(err)
	}
	e.stdin("")
	code, _, stderr := loomux("attach", "--force", "--task", e.task.ID)
	if code != 0 || strings.Contains(stderr, "[y/N]") {
		t.Errorf("--force: exit %d, stderr:\n%s", code, stderr)
	}
}

// Without a terminal there is nothing to attach: it stops before
// anything is taken.
func TestAttachNeedsATerminal(t *testing.T) {
	e := newAttachEnv(t)
	e.ssh("touch " + e.file("ran"))
	e.terminal(false)
	code, _, stderr := loomux("attach", "--task", e.task.ID)
	if code != 1 || !strings.Contains(stderr, "terminal") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
	if e.ran("ran") || e.srv.Task(e.task.ID).Status != registry.TaskStatusAwaitingInput {
		t.Error("something was taken or run without a terminal")
	}
}

func TestAttachUsage(t *testing.T) {
	newAttachEnv(t)
	for _, args := range [][]string{
		{"attach"},
		{"attach", "a", "b"},
		{"attach", "--task", "a", "b"},
		{"attach", "--task"},
		{"attach", "--task", ""},
		{"attach", ""},
		{"attach", "--nope", "a"},
	} {
		code, stdout, stderr := loomux(args...)
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "usage: loomux") || !strings.Contains(stderr, "attach") {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestAttachNotSignedIn(t *testing.T) {
	e := newAttachEnv(t)
	e.ssh("touch " + e.file("ran"))
	// No stored token.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	code, _, stderr := loomux("attach", "--task", e.task.ID)
	if code != 1 || !strings.Contains(stderr, "not signed in") || !strings.Contains(stderr, "loomux login") {
		t.Errorf("no token: exit %d, stderr %q", code, stderr)
	}
}

// A token the server no longer accepts.
func TestAttachSignedOut(t *testing.T) {
	e := newAttachEnv(t)
	e.ssh("touch " + e.file("ran"))
	mine, err := client.New(e.srv.URL, e.token)
	if err != nil {
		t.Fatal(err)
	}
	if err := mine.Logout(attachCtx(t)); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := loomux("attach", "--task", e.task.ID)
	if code != 1 || !strings.Contains(stderr, "loomux login") || e.ran("ran") {
		t.Errorf("exit %d, ran %v, stderr %q", code, e.ran("ran"), stderr)
	}
	if strings.Contains(stderr, e.token) {
		t.Error("the token is in the output")
	}
}

// A server from before holds: its command is shown as text, never run.
func TestAttachOldServer(t *testing.T) {
	e := newAttachEnv(t)
	e.ssh("touch " + e.file("ran"))
	e.refuse = func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, "/takeover") {
			return http.StatusNotFound
		}
		return 0
	}
	code, stdout, stderr := loomux("attach", "--task", e.task.ID)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	for _, want := range []string{"upgrade", "ssh loomux@box.test -t 'tmux -L " + apitest.TmuxSocket + " attach -t loomux-" + e.task.ID + "'"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr has no %q:\n%s", want, stderr)
		}
	}
	if e.ran("ran") || stdout != "" {
		t.Errorf("the command ran without a hold (stdout %q)", stdout)
	}
}

// Nothing this machine can run: the commands are shown with why, and
// nothing is taken.
func TestAttachNoCommand(t *testing.T) {
	e := newAttachEnv(t) // no ssh on PATH
	code, _, stderr := loomux("attach", "--task", e.task.ID)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	for _, want := range []string{"ssh loomux@box.test -t", "PATH"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr has no %q:\n%s", want, stderr)
		}
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusAwaitingInput {
		t.Errorf("status = %s: something was taken", st)
	}
}

// [attach] in the config reaches the choice of a command.
func TestAttachConfig(t *testing.T) {
	e := newAttachEnv(t)
	e.ssh("touch " + e.file("ran"))
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "loomux", "config.toml")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(f, "\n[attach]\nprefer = [\"kubectl\"]\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	code, _, stderr := loomux("attach", "--task", e.task.ID)
	if code != 1 || !strings.Contains(stderr, "prefer") || e.ran("ran") {
		t.Errorf("exit %d, ran %v, stderr:\n%s", code, e.ran("ran"), stderr)
	}
}

// A signal ends the command and releases the hold; the exit status says
// which signal.
func TestAttachReleasesOnASignal(t *testing.T) {
	e := newAttachEnv(t)
	e.ssh("touch " + e.file("started") + "; exec sleep 60")
	type result struct {
		code   int
		stderr string
	}
	done := make(chan result, 1)
	go func() {
		code, _, stderr := loomux("attach", "--task", e.task.ID)
		done <- result{code, stderr}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for !e.ran("started") {
		if time.Now().After(deadline) {
			t.Fatal("the command didn't start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h := e.srv.ActiveHold(e.task.ID); h == nil {
		t.Fatal("no hold while attached")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.code != 128+int(syscall.SIGTERM) {
			t.Errorf("exit %d, want %d; stderr:\n%s", r.code, 128+int(syscall.SIGTERM), r.stderr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("attach didn't end after the signal")
	}
	if h := e.srv.ActiveHold(e.task.ID); h != nil {
		t.Errorf("still held after the signal: %+v", h)
	}
	if st := e.srv.Task(e.task.ID).Status; st != registry.TaskStatusAwaitingInput {
		t.Errorf("status = %s, want it handed back", st)
	}
}

func TestAsk(t *testing.T) {
	for _, tc := range []struct {
		typed string
		def   bool
		want  bool
	}{
		{"y\n", false, true}, {"Y\n", false, true}, {"yes\n", false, true}, {" yes \r\n", false, true},
		{"n\n", true, false}, {"no\n", true, false}, {"N\n", true, false},
		{"\n", true, true}, {"\n", false, false},
		{"maybe\n", true, false}, {"maybe\n", false, false},
		// No answer at all (the terminal is gone) is never a yes.
		{"", true, false}, {"", false, false},
		{"y", false, true},
	} {
		var out bytes.Buffer
		if got := ask(context.Background(), strings.NewReader(tc.typed), &out, "Go on?", tc.def); got != tc.want {
			t.Errorf("typed %q (default %v): %v, want %v", tc.typed, tc.def, got, tc.want)
		}
		want := "Go on? [y/N] "
		if tc.def {
			want = "Go on? [Y/n] "
		}
		if out.String() != want {
			t.Errorf("the question = %q, want %q", out.String(), want)
		}
	}
	// It reads its own line and no further: what follows is the next
	// program's.
	in := strings.NewReader("y\nrest")
	ask(context.Background(), in, io.Discard, "Go on?", false)
	if rest, _ := io.ReadAll(in); string(rest) != "rest" {
		t.Errorf("after the answer the input has %q left, want %q", rest, "rest")
	}
}

// A question left unanswered ends when the context does (a signal): the
// client doesn't sit at a prompt it was told to leave.
func TestAskEndsWhenCancelled(t *testing.T) {
	never, w := io.Pipe() // an input nobody types into
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	answered := make(chan bool, 1)
	go func() { answered <- ask(ctx, never, io.Discard, "Go on?", true) }()
	select {
	case got := <-answered:
		t.Fatalf("ask answered %v with nothing typed", got)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case got := <-answered:
		if got {
			t.Error("a cancelled question was a yes")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ask didn't end when its context was cancelled")
	}
}

// A signal while the question about a held task is on screen ends the
// client at once, with nothing taken.
func TestAttachSignalAtTheHeldQuestion(t *testing.T) {
	e := newAttachEnv(t)
	e.ssh("touch " + e.file("ran"))
	theirs, err := e.other("desk").Takeover(attachCtx(t), e.task.ID, client.TakeoverRequest{})
	if err != nil {
		t.Fatal(err)
	}
	never, w := io.Pipe()
	defer w.Close()
	old := stdin
	stdin = never
	defer func() { stdin = old }()

	var stderr lockedBuffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"attach", "--task", e.task.ID}, io.Discard, &stderr) }()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(stderr.String(), "[y/N]") {
		if time.Now().After(deadline) {
			t.Fatalf("no question:\n%s", stderr.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 128+int(syscall.SIGTERM) {
			t.Errorf("exit %d, want %d; stderr:\n%s", code, 128+int(syscall.SIGTERM), stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the client stayed at the question after the signal")
	}
	if h := e.srv.ActiveHold(e.task.ID); h == nil || h.ID != theirs.Hold.ID {
		t.Errorf("the hold = %+v, want desk's still", h)
	}
	if e.ran("ran") {
		t.Error("the command ran")
	}
}

// lockedBuffer is a bytes.Buffer two goroutines may write to: the
// command's output is copied in beside attach's own lines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

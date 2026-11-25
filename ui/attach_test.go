//go:build unix

package ui

// Attaching from the list, against a real api.Server from the pinned
// server module (api/apitest): the takeover, heartbeat and release are
// the server's own. The attach command is a fake: a script called ssh,
// the only program on PATH. Handing the terminal over (tea.Exec) and
// opening a tmux window are the two steps a test stands in for.

import (
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
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"

	"github.com/Loomux/tui/attach"
	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
)

// attachWorld is a list on a test server with one conversation, whose
// agent is open, and the fake ssh's directory as PATH.
type attachWorld struct {
	*harness
	srv  *apitest.Server
	api  *client.Client
	task *registry.Task
	bin  string

	// refuse, when set, answers a request before the server sees it.
	refuse atomic.Pointer[func(*http.Request) int]
	// polls counts the list's polls.
	polls atomic.Int32

	// The terminal, as the attach saw it: what was typed is typed, and
	// out is everything written to it.
	typed string
	out   lockedBuffer
	// windows are the tmux windows asked for; windowErr fails them.
	// closed are the ones closed again, by id.
	windows   []window
	windowErr error
	closed    []string
}

type window struct {
	name string
	env  []string
	argv []string
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
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

// countedAPI counts the list's polls on their way to the client.
type countedAPI struct {
	*client.Client
	polls *atomic.Int32
}

func (c countedAPI) Conversations(ctx context.Context, limit int) (*client.ConversationPage, error) {
	c.polls.Add(1)
	return c.Client.Conversations(ctx, limit)
}

var windowCommand = []string{"/usr/local/bin/loomux", "attach", "--profile", "test", "--keep-open"}

func newAttachWorld(t *testing.T, mode string, inTmux bool) *attachWorld {
	t.Helper()
	w := &attachWorld{srv: apitest.New(t), bin: t.TempDir()}
	w.task = w.srv.SeedConversation("", "fix the build", "on it")
	front := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if f := w.refuse.Load(); f != nil {
			if status := (*f)(r); status != 0 {
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(status)
				_, _ = rw.Write([]byte(`{"error":"not found","code":"not_found"}`))
				return
			}
		}
		w.srv.API.ServeHTTP(rw, r)
	}))
	t.Cleanup(front.Close)
	var err error
	if w.api, err = client.New(front.URL, w.srv.SeedLinkedSession("laptop", registry.SessionKindTUI)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", w.bin)

	h := &harness{t: t, now: t0}
	h.app = New(Options{
		Profile: "test", API: countedAPI{w.api, &w.polls}, Keys: keymap.Default(), Now: func() time.Time { return h.now },
		Attach: &Attach{API: w.api, Mode: mode, InTmux: inTmux, Window: windowCommand,
			WindowEnv: []string{"LOOMUX_SERVER=https://loomux.example.net"}},
	})
	h.app.tick = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return firedTick{d, fn(h.now.Add(d))} }
	}
	// The terminal is handed over at once, to the test's own streams.
	h.app.exec = func(c tea.ExecCommand, fn tea.ExecCallback) tea.Cmd {
		return func() tea.Msg {
			c.SetStdin(strings.NewReader(w.typed))
			c.SetStdout(&w.out)
			c.SetStderr(&w.out)
			return fn(c.Run())
		}
	}
	h.app.newWindow = func(_ context.Context, name string, env, argv []string) (string, error) {
		w.windows = append(w.windows, window{name, slices.Clone(env), slices.Clone(argv)})
		if w.windowErr != nil {
			return "", w.windowErr
		}
		return "@7", nil
	}
	h.app.closeWindow = func(_ context.Context, id string) error {
		w.closed = append(w.closed, id)
		return nil
	}
	t.Cleanup(func() { _ = h.app.Close() })
	w.harness = h
	h.run(h.app.Init())
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.flush()
	if got := h.selected(); got != w.task.ConversationID {
		t.Fatalf("the list's row is %q, want the seeded conversation %q:\n%s", got, w.task.ConversationID, h.frame())
	}
	return w
}

// ssh puts the fake ssh on PATH.
func (w *attachWorld) ssh(body string) {
	w.t.Helper()
	script := "#!/bin/sh\nPATH=/usr/bin:/bin\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(w.bin, "ssh"), []byte(script), 0o755); err != nil {
		w.t.Fatal(err)
	}
}

func (w *attachWorld) file(name string) string { return filepath.Join(w.bin, name) }

func (w *attachWorld) ran(name string) bool {
	_, err := os.Stat(w.file(name))
	return err == nil
}

func (w *attachWorld) other(label string) *client.Client {
	w.t.Helper()
	c, err := client.New(w.srv.URL, w.srv.SeedLinkedSession(label, registry.SessionKindTUI))
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

func (w *attachWorld) status() registry.TaskStatus { return w.srv.Task(w.task.ID).Status }

func bg(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// footer is the screen's last line.
func (w *attachWorld) footer() string {
	lines := w.lines()
	return lines[len(lines)-1]
}

// a on a row: takeover, the terminal handed to the attach command,
// release, the screens back with what happened, and the list polled at
// once.
func TestAttachInline(t *testing.T) {
	w := newAttachWorld(t, "inline", true)
	w.ssh(`printf '%s\n' "$@" > ` + w.file("args") + `; echo in the pane`)
	polls := w.polls.Load()
	w.key("a")

	args, err := os.ReadFile(w.file("args"))
	if err != nil {
		t.Fatalf("the attach command didn't run: %v\n%s", err, w.frame())
	}
	if !strings.Contains(string(args), "attach -t loomux-"+w.task.ID+"\n") {
		t.Errorf("ssh ran with %q, want the task's session", args)
	}
	out := w.out.String()
	if !strings.Contains(out, "attaching to ") || !strings.Contains(out, " via ssh") || !strings.Contains(out, "in the pane") {
		t.Errorf("the terminal got %q", out)
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held after the attach: %+v", h)
	}
	if st := w.status(); st != registry.TaskStatusAwaitingInput {
		t.Errorf("status = %s, want awaiting-input", st)
	}
	if f := w.footer(); !strings.Contains(f, "detached") || !strings.Contains(f, "released") {
		t.Errorf("the footer doesn't say what happened: %q", f)
	}
	if w.polls.Load() <= polls {
		t.Error("the list wasn't polled after the attach")
	}
	if len(w.windows) != 0 {
		t.Errorf("a tmux window was asked for in inline mode: %+v", w.windows)
	}
}

// While attached the task is held, by this client.
func TestAttachInlineHoldsWhileAttached(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("started") + "; while [ ! -e " + w.file("detach") + " ]; do sleep 0.02; done")
	seen := make(chan *registry.TaskHold, 1)
	go func() {
		for !w.ran("started") {
			time.Sleep(5 * time.Millisecond)
		}
		seen <- w.srv.ActiveHold(w.task.ID)
		_ = os.WriteFile(w.file("detach"), nil, 0o644)
	}()
	w.key("a")
	if h := <-seen; h == nil || h.ClientLabel != "laptop" {
		t.Errorf("the hold while attached = %+v, want the laptop's", h)
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
}

func TestAttachNothingToAttachTo(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	w.srv.SetTaskStatus(w.task.ID, registry.TaskStatusCompleted, nil)
	w.key("a")
	if f := w.footer(); !strings.Contains(f, "no agent to attach to") {
		t.Errorf("footer = %q", f)
	}
	if w.ran("ran") {
		t.Error("the command ran")
	}
}

// Another session holds the task: the screen names it and asks before
// taking it.
func TestAttachHeldAsks(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	desk := w.other("desk")
	theirs, err := desk.Takeover(bg(t), w.task.ID, client.TakeoverRequest{})
	if err != nil {
		t.Fatal(err)
	}

	for _, no := range []string{"n", "esc", "q", "enter"} {
		w.key("a")
		f := w.frame()
		if !strings.Contains(f, "driven from the terminal on desk") || !strings.Contains(f, "Take it over?") {
			t.Fatalf("the screen doesn't name the holder and ask:\n%s", f)
		}
		if !strings.Contains(w.footer(), "y ") || !strings.Contains(w.footer(), "n ") {
			t.Errorf("the footer doesn't give the answers' keys: %q", w.footer())
		}
		w.send(keyPress(no))
		if slices.ContainsFunc(w.out2(), func(m tea.Msg) bool { _, ok := m.(tea.QuitMsg); return ok }) {
			t.Fatalf("%s quit the screens instead of answering the question", no)
		}
		w.flush()
		if f := w.frame(); strings.Contains(f, "Take it over?") || !strings.Contains(f, "fix the build") {
			t.Fatalf("%s: the list isn't back:\n%s", no, f)
		}
		if h := w.srv.ActiveHold(w.task.ID); h == nil || h.ID != theirs.Hold.ID {
			t.Fatalf("%s: the hold = %+v, want desk's still", no, h)
		}
	}
	if w.ran("ran") {
		t.Fatal("the command ran without the task being taken")
	}

	w.key("a")
	w.key("y")
	if !w.ran("ran") {
		t.Fatalf("the command didn't run after a yes:\n%s", w.frame())
	}
	if _, err := desk.Heartbeat(bg(t), w.task.ID, theirs.Hold.ID); !client.Is(err, client.CodeHoldEnded) {
		t.Errorf("desk's hold after the takeover: %v, want hold_ended", err)
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held after the attach: %+v", h)
	}
}

// out2 is the messages the last commands yielded and nothing fed back.
func (w *attachWorld) out2() []tea.Msg { return w.harness.out }

// A server from before holds: its command is shown as text on the
// screen, and never run.
func TestAttachOldServerShowsTheCommand(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	refuse := func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, "/takeover") {
			return http.StatusNotFound
		}
		return 0
	}
	w.refuse.Store(&refuse)
	w.key("a")
	f := w.frame()
	for _, want := range []string{"can't attach yet", "upgrade", "ssh loomux@"} {
		if !strings.Contains(f, want) {
			t.Errorf("the screen has no %q:\n%s", want, f)
		}
	}
	// The whole command is there to copy, wrapped if it is long.
	if joined := strings.Join(strings.Fields(f), " "); !strings.Contains(strings.ReplaceAll(joined, " ", ""), strings.ReplaceAll("attach -t loomux-"+w.task.ID, " ", "")) {
		t.Errorf("the command is cut:\n%s", f)
	}
	if w.ran("ran") {
		t.Error("the command ran without a hold")
	}
	// Any key puts the list back.
	w.key("j")
	if f := w.frame(); strings.Contains(f, "can't attach yet") || !strings.Contains(f, "fix the build") {
		t.Errorf("the list isn't back:\n%s", f)
	}
}

// Nothing this machine can run: the screen shows the commands and why,
// and nothing is taken.
func TestAttachNoCommandShowsWhy(t *testing.T) {
	w := newAttachWorld(t, "inline", false) // no ssh on PATH
	w.key("a")
	f := w.frame()
	for _, want := range []string{"ssh loomux@", "PATH"} {
		if !strings.Contains(f, want) {
			t.Errorf("the screen has no %q:\n%s", want, f)
		}
	}
	if st := w.status(); st != registry.TaskStatusAwaitingInput {
		t.Errorf("status = %s: something was taken", st)
	}
}

// The takeover's commands are none this machine can run, and the hold it
// took couldn't be given back (attach.Take joins the two): the screen
// doesn't say nothing was taken.
func TestAttachNoCommandAfterTheTakeover(t *testing.T) {
	h := loaded(t, 80, 24)
	none := &attach.NoCommandError{Skipped: []attach.Skipped{{Via: "ssh", Line: "ssh loomux@box.example.net", Reason: "ssh isn't on PATH"}}}
	h.app.showAttachError(errors.Join(none, &attach.ReleaseError{Err: errors.New("connection refused"), Expires: t0}))
	f := h.frame()
	if strings.Contains(f, "Nothing was taken") || !strings.Contains(f, "Couldn't release the hold") {
		t.Errorf("the screen doesn't say the hold is still held:\n%s", f)
	}
	if !strings.Contains(f, "ssh loomux@box.example.net") {
		t.Errorf("the commands aren't shown:\n%s", f)
	}
}

// The hold ended another way while attached: the screens come back
// saying which.
func TestAttachSaysHowTheHoldEnded(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("started") + "; while [ ! -e " + w.file("detach") + " ]; do sleep 0.02; done")
	phone := w.other("phone")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !w.ran("started") {
			time.Sleep(5 * time.Millisecond)
		}
		// Taken back from the phone; the server then detaches the
		// terminal, which is the command ending.
		if _, err := phone.Release(bg(t), w.task.ID, ""); err != nil {
			t.Errorf("take back: %v", err)
		}
		_ = os.WriteFile(w.file("detach"), nil, 0o644)
	}()
	w.key("a")
	<-done
	if f := w.footer(); !strings.Contains(f, "taken back") {
		t.Errorf("footer = %q, want it to say the task was taken back", f)
	}
}

// A command that fails fast is followed by a question on the terminal
// about the next; here there is none, so the screens come back with the
// failure.
func TestAttachCommandFails(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("echo no route >&2; exit 255")
	w.key("a")
	if f := w.footer(); !strings.Contains(f, "couldn't attach") || !strings.Contains(f, "255") {
		t.Errorf("footer = %q", f)
	}
	if !strings.Contains(w.out.String(), "no route") {
		t.Errorf("the terminal got %q", w.out.String())
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
}

// Inside tmux the attach gets a window of its own, which holds and
// releases by itself: the screens take nothing and stay up.
func TestAttachWindow(t *testing.T) {
	w := newAttachWorld(t, "auto", true)
	w.ssh("touch " + w.file("ran"))
	polls := w.polls.Load()
	w.key("a")
	if len(w.windows) != 1 {
		t.Fatalf("%d tmux windows asked for, want 1:\n%s", len(w.windows), w.frame())
	}
	win := w.windows[0]
	if want := append(slices.Clone(windowCommand), "--task", w.task.ID); !slices.Equal(win.argv, want) {
		t.Errorf("the window runs %q, want %q", win.argv, want)
	}
	if win.name != "apitest" {
		t.Errorf("the window is named %q, want the workspace's name", win.name)
	}
	if !slices.Equal(win.env, []string{"LOOMUX_SERVER=https://loomux.example.net"}) {
		t.Errorf("the window's environment = %q", win.env)
	}
	if w.ran("ran") || w.srv.ActiveHold(w.task.ID) != nil {
		t.Error("the screens attached themselves as well as opening a window")
	}
	if f := w.footer(); !strings.Contains(f, "tmux window") {
		t.Errorf("footer = %q", f)
	}
	if w.polls.Load() <= polls {
		t.Error("the list wasn't polled after the window opened")
	}
}

// The window is named for the workspace even when a is pressed before
// the names have come back: it waits for GET /workspaces rather than
// name the window after the start of the workspace's id.
func TestAttachWindowWaitsForTheWorkspaceName(t *testing.T) {
	w := newAttachWorld(t, "auto", true)
	w.ssh("touch " + w.file("ran"))
	// The names haven't arrived: the row shows the id's start, and the
	// attach key is pressed now, with GET /workspaces still out.
	w.app.names = map[string]string{}
	w.app.home.setNames(nil)
	w.app.namesBusy = true
	w.key("a")
	if len(w.windows) != 0 {
		t.Fatalf("the window opened before the names came: %+v", w.windows)
	}
	w.send(namesMsg{workspaces: []client.Workspace{{ID: w.task.WorkspaceID, Name: "apitest"}}})
	w.flush()
	if len(w.windows) != 1 {
		t.Fatalf("%d tmux windows asked for, want 1:\n%s", len(w.windows), w.frame())
	}
	if got := w.windows[0].name; got != "apitest" {
		t.Errorf("the window is named %q, want the workspace's name, not the id's start", got)
	}
}

// The names may answer before the conversation does: the window still
// takes the name that arrived, not the one the row showed when a was
// pressed.
func TestAttachWindowUsesNamesThatArriveBeforeTheTask(t *testing.T) {
	w := newAttachWorld(t, "auto", true)
	w.ssh("touch " + w.file("ran"))
	// The conversation request is in flight: its answer isn't handled
	// yet, and the names answer passes it.
	w.app.names = map[string]string{}
	w.app.home.setNames(nil)
	w.send(keyPress("a"))
	w.send(namesMsg{workspaces: []client.Workspace{{ID: w.task.WorkspaceID, Name: "apitest"}}})
	w.flush()
	if len(w.windows) != 1 {
		t.Fatalf("%d tmux windows asked for, want 1:\n%s", len(w.windows), w.frame())
	}
	if got := w.windows[0].name; got != "apitest" {
		t.Errorf("the window is named %q, want the workspace's name, not the id's start", got)
	}
}

// Esc while the window waits for the names settles the start at once:
// the next a isn't ignored while the names answer is outstanding.
func TestAttachCancelledWhileWaitingForTheNames(t *testing.T) {
	w := newAttachWorld(t, "auto", true)
	w.ssh("touch " + w.file("ran"))
	// GET /workspaces is out: the first a waits for it.
	w.app.names = map[string]string{}
	w.app.home.setNames(nil)
	w.app.namesBusy = true
	w.key("a")
	w.key("esc")
	// Another a before the names answer: a start of its own, not ignored.
	w.key("a")
	w.send(namesMsg{workspaces: []client.Workspace{{ID: w.task.WorkspaceID, Name: "apitest"}}})
	w.flush()
	if len(w.windows) != 1 {
		t.Fatalf("%d tmux windows asked for, want 1:\n%s", len(w.windows), w.frame())
	}
	if got := w.windows[0].name; got != "apitest" {
		t.Errorf("the window is named %q, want the workspace's name", got)
	}
}

// A refreshed names answer that omits the workspace takes the window back
// to the id's start, as the list does, rather than keeping the cached
// name.
func TestAttachWindowUsesRefreshedNamesThatOmitTheWorkspace(t *testing.T) {
	w := newAttachWorld(t, "auto", true)
	w.ssh("touch " + w.file("ran"))
	// The task request is in flight; a captured the cached name.
	w.send(keyPress("a"))
	// A successful refresh no longer lists the conversation's workspace.
	w.send(namesMsg{workspaces: []client.Workspace{{ID: "another", Name: "another"}}})
	w.flush()
	if len(w.windows) != 1 {
		t.Fatalf("%d tmux windows asked for, want 1:\n%s", len(w.windows), w.frame())
	}
	if got, want := w.windows[0].name, workspaceName(w.task.WorkspaceID, nil); got != want {
		t.Errorf("the window is named %q, want %q: the refreshed names have no entry for the workspace", got, want)
	}
}

// A window that can't be opened isn't the end: the attach happens here.
func TestAttachWindowFallsBackInline(t *testing.T) {
	w := newAttachWorld(t, "window", true)
	w.ssh("touch " + w.file("ran"))
	w.windowErr = errors.New("tmux new-window: exit status 1: no space for new window")
	w.key("a")
	if len(w.windows) != 1 || !w.ran("ran") {
		t.Fatalf("windows asked for %d, ran inline %v:\n%s", len(w.windows), w.ran("ran"), w.frame())
	}
	if out := w.out.String(); !strings.Contains(out, "no space for new window") || !strings.Contains(out, "attaching here") {
		t.Errorf("the terminal doesn't say why it attached here: %q", out)
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("still held: %+v", h)
	}
}

func TestAttachModes(t *testing.T) {
	for _, tc := range []struct {
		mode       string
		inTmux     bool
		wantWindow bool
	}{
		{"auto", true, true},
		{"auto", false, false},
		{"", true, true}, // unset is auto
		{"inline", true, false},
		{"inline", false, false},
		{"window", true, true},
		// No tmux to open a window in: here, then.
		{"window", false, false},
	} {
		w := newAttachWorld(t, tc.mode, tc.inTmux)
		w.ssh("touch " + w.file("ran"))
		w.key("a")
		if got := len(w.windows) == 1; got != tc.wantWindow || w.ran("ran") == tc.wantWindow {
			t.Errorf("mode %q, in tmux %v: window %v, inline %v; want window %v", tc.mode, tc.inTmux, got, w.ran("ran"), tc.wantWindow)
		}
	}
}

// esc while the attach is starting stops it, whatever it had got to: a
// hold already taken is given back.
func TestAttachCancelledWhileStarting(t *testing.T) {
	// Before the conversation's task is known.
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	w.send(keyPress("a")) // the answer waits in w.out
	w.send(keyPress("esc"))
	w.flush()
	if w.ran("ran") || w.srv.ActiveHold(w.task.ID) != nil {
		t.Errorf("cancelled before the takeover: ran %v, hold %+v", w.ran("ran"), w.srv.ActiveHold(w.task.ID))
	}
	if f := w.footer(); !strings.Contains(f, "cancelled") {
		t.Errorf("footer = %q", f)
	}

	// After the takeover was answered, before the terminal was handed over.
	w = newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	w.send(keyPress("a"))
	w.feedOne() // the task: the takeover runs, its answer waits
	if h := w.srv.ActiveHold(w.task.ID); h == nil {
		t.Fatal("no hold after the takeover")
	}
	w.send(keyPress("esc"))
	w.flush()
	if w.ran("ran") {
		t.Error("the command ran after the cancel")
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("the hold wasn't given back: %+v", h)
	}
	if st := w.status(); st != registry.TaskStatusAwaitingInput {
		t.Errorf("status = %s, want it handed back", st)
	}
}

// esc after the takeover, and the hold can't be given back: the footer
// says so, and when it runs out.
func TestAttachCancelledReportsAFailedRelease(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	refuse := func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, "/release") {
			return http.StatusNotFound
		}
		return 0
	}
	w.refuse.Store(&refuse)
	w.send(keyPress("a"))
	w.feedOne() // the task: the takeover runs, its answer waits
	w.send(keyPress("esc"))
	w.flush()
	if h := w.srv.ActiveHold(w.task.ID); h == nil {
		t.Fatal("the release went through: nothing to report")
	}
	if f := w.footer(); !strings.Contains(f, "cancelled") || !strings.Contains(f, "couldn't release") {
		t.Errorf("footer = %q, want the cancel and the failed release", f)
	}
	if w.ran("ran") {
		t.Error("the command ran after the cancel")
	}
	if err := w.app.Close(); err != nil {
		t.Errorf("Close reports a failure the footer showed: %v", err)
	}

	// The screens quit before the failed release's answer reached them:
	// Close reports it.
	w = newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	w.refuse.Store(&refuse)
	w.send(keyPress("a"))
	w.feedOne() // the task: the takeover runs, its answer waits
	w.send(keyPress("esc"))
	w.feedOne() // the takeover's answer: the hold is given back, which fails
	if h := w.srv.ActiveHold(w.task.ID); h == nil {
		t.Fatal("no hold: nothing to give back")
	}
	var rel *attach.ReleaseError
	if err := w.app.Close(); !errors.As(err, &rel) {
		t.Errorf("Close = %v, want the failed release the screens didn't get to show", err)
	}
}

// The takeover took a hold it then couldn't give back (attach.Take: here a
// hold that isn't a drive, whose release fails). That is said wherever
// the attach had got to: on screen, after esc, and by Close when the
// screens quit before the takeover's answer. Once, each time.
func TestAttachTakeoverThatCouldntGiveBack(t *testing.T) {
	setup := func() *attachWorld {
		w := newAttachWorld(t, "inline", false)
		w.ssh("touch " + w.file("ran"))
		w.app.attach.API = watchTakeover{w.app.attach.API}
		refuse := func(r *http.Request) int {
			if strings.HasSuffix(r.URL.Path, "/release") {
				return http.StatusNotFound
			}
			return 0
		}
		w.refuse.Store(&refuse)
		return w
	}

	// Answered: on screen, whole, and Close doesn't say it again.
	w := setup()
	w.key("a")
	if h := w.srv.ActiveHold(w.task.ID); h == nil {
		t.Fatal("the release went through: nothing to report")
	}
	if f := w.frame(); !strings.Contains(f, "Couldn't release the hold") || !strings.Contains(f, "expires by itself") {
		t.Errorf("the screen doesn't say the hold is still held:\n%s", f)
	}
	if err := w.app.Close(); err != nil {
		t.Errorf("Close reports what the screen showed: %v", err)
	}
	if w.ran("ran") {
		t.Error("the command ran")
	}

	// esc while the takeover was on its way.
	w = setup()
	w.send(keyPress("a"))
	w.feedOne() // the task: the takeover runs, its answer waits
	w.send(keyPress("esc"))
	w.flush()
	if f := w.footer(); !strings.Contains(f, "cancelled") || !strings.Contains(f, "couldn't release") {
		t.Errorf("footer = %q, want the cancel and the failed release", f)
	}
	if err := w.app.Close(); err != nil {
		t.Errorf("Close reports what the footer showed: %v", err)
	}

	// The screens quit before the takeover's answer reached them.
	w = setup()
	w.send(keyPress("a"))
	w.feedOne() // the task: the takeover runs, its answer waits
	var rel *attach.ReleaseError
	if err := w.app.Close(); !errors.As(err, &rel) {
		t.Errorf("Close = %v, want the failed release", err)
	}
}

// Bubble Tea couldn't hand the terminal over, so the attach never ran: the
// hold taken for it is given back, and the footer says why; a failed
// give-back is said too.
func TestAttachTerminalNotHandedOver(t *testing.T) {
	setup := func() *attachWorld {
		w := newAttachWorld(t, "inline", false)
		w.ssh("touch " + w.file("ran"))
		w.app.exec = func(_ tea.ExecCommand, fn tea.ExecCallback) tea.Cmd {
			return func() tea.Msg { return fn(errors.New("can't release the terminal")) }
		}
		return w
	}
	w := setup()
	w.key("a")
	if w.ran("ran") {
		t.Error("the command ran")
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("the hold wasn't given back: %+v", h)
	}
	if f := w.footer(); !strings.Contains(f, "couldn't attach") || !strings.Contains(f, "can't release the terminal") {
		t.Errorf("footer = %q, want why", f)
	}
	if st := w.status(); st != registry.TaskStatusAwaitingInput {
		t.Errorf("status = %s, want it handed back", st)
	}

	w = setup()
	refuse := func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, "/release") {
			return http.StatusNotFound
		}
		return 0
	}
	w.refuse.Store(&refuse)
	w.key("a")
	if h := w.srv.ActiveHold(w.task.ID); h == nil {
		t.Fatal("the release went through: nothing to report")
	}
	if f := w.footer(); !strings.Contains(f, "couldn't attach") || !strings.Contains(f, "couldn't release the hold") {
		t.Errorf("footer = %q, want the failed release", f)
	}
	if err := w.app.Close(); err != nil {
		t.Errorf("Close reports what the footer showed: %v", err)
	}
}

// watchTakeover answers a takeover with its hold relabelled as a watch,
// which attach.Take gives back at once.
type watchTakeover struct{ AttachAPI }

func (a watchTakeover) Takeover(ctx context.Context, taskID string, r client.TakeoverRequest) (*client.Takeover, error) {
	tk, err := a.AttachAPI.Takeover(ctx, taskID, r)
	if err == nil {
		tk.Hold.Mode = client.ModeWatch
	}
	return tk, err
}

// esc while the tmux window is being opened: a window that opened all
// the same is closed again, so nothing attaches after the list said the
// attach was cancelled.
func TestAttachCancelledWhileTheWindowOpens(t *testing.T) {
	w := newAttachWorld(t, "auto", true)
	w.ssh("touch " + w.file("ran"))
	w.send(keyPress("a"))
	w.feedOne() // the task: the window is asked for, its answer waits
	if len(w.windows) != 1 {
		t.Fatalf("%d windows asked for, want 1", len(w.windows))
	}
	w.send(keyPress("esc"))
	w.flush()
	if !slices.Equal(w.closed, []string{"@7"}) {
		t.Errorf("windows closed = %q, want the one that opened", w.closed)
	}
	if f := w.footer(); strings.Contains(f, "attaching in a new tmux window") {
		t.Errorf("the footer says it is attaching after the cancel: %q", f)
	}
	if w.ran("ran") || w.srv.ActiveHold(w.task.ID) != nil {
		t.Error("the screens attached after the cancel")
	}

	// A window that opens and isn't cancelled stays.
	w = newAttachWorld(t, "auto", true)
	w.key("a")
	if len(w.windows) != 1 || len(w.closed) != 0 {
		t.Errorf("windows %d, closed %q; want one open", len(w.windows), w.closed)
	}
}

// feedOne feeds back the first message the last commands yielded.
func (w *attachWorld) feedOne() {
	w.t.Helper()
	if len(w.harness.out) == 0 {
		w.t.Fatal("no message to feed")
	}
	msg := w.harness.out[0]
	w.harness.out = w.harness.out[1:]
	w.send(msg)
}

// a again while an attach is starting changes nothing: one takeover,
// one command.
func TestAttachTwice(t *testing.T) {
	for _, when := range []string{"while the task is looked up", "while the takeover's answer waits"} {
		w := newAttachWorld(t, "inline", false)
		w.ssh("echo ran >> " + w.file("runs"))
		var takeovers atomic.Int32
		count := func(r *http.Request) int {
			if strings.HasSuffix(r.URL.Path, "/takeover") {
				takeovers.Add(1)
			}
			return 0
		}
		w.refuse.Store(&count)
		w.send(keyPress("a"))
		if when == "while the takeover's answer waits" {
			w.feedOne() // the task: the takeover runs, its answer waits
		}
		w.send(keyPress("a"))
		w.flush()
		runs, _ := os.ReadFile(w.file("runs"))
		if string(runs) != "ran\n" || takeovers.Load() != 1 {
			t.Errorf("a again %s: the command ran %q after %d takeovers, want once each", when, runs, takeovers.Load())
		}
		if h := w.srv.ActiveHold(w.task.ID); h != nil {
			t.Errorf("a again %s: still held: %+v", when, h)
		}
	}
}

// esc doesn't abort a takeover on its way to the server: its answer is
// the only way to learn the hold to give back. The request is left to
// finish.
func TestAttachCancelLetsTheTakeoverFinish(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	w.send(keyPress("a"))
	w.send(keyPress("esc")) // before the takeover is even sent
	w.flush()               // the task's answer: dropped, nothing taken
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Fatalf("a takeover was sent for a cancelled attach: %+v", h)
	}
	// A new attach may start once the cancelled one has settled.
	w.key("a")
	if !w.ran("ran") {
		t.Errorf("a new attach after the cancel didn't run:\n%s", w.frame())
	}

	w = newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	w.send(keyPress("a"))
	st := w.app.starting
	w.feedOne()
	w.send(keyPress("esc"))
	if st.ctx.Err() != nil {
		t.Error("esc cancelled the context of a takeover that may be on its way")
	}
	w.flush()
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("the hold wasn't given back: %+v", h)
	}
}

// The screens quit with a hold taken and the terminal not yet handed
// over: Close gives it back.
func TestCloseReleasesAHoldNotYetAttached(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	w.send(keyPress("a"))
	w.feedOne()
	if h := w.srv.ActiveHold(w.task.ID); h == nil {
		t.Fatal("no hold after the takeover")
	}
	w.app.Close()
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("Close left the hold: %+v", h)
	}
	if w.ran("ran") {
		t.Error("the command ran")
	}
}

// A release that fails where the screens can't say so any more is Close's
// to report: after a signal quit the screens during the attach, and for a
// hold Close gives back itself.
func TestCloseReportsAFailedRelease(t *testing.T) {
	failed := &attach.ReleaseError{Err: errors.New("connection refused"), Expires: t0}
	w := newAttachWorld(t, "inline", false)
	w.send(attachedMsg{err: errors.Join(context.Canceled, failed), sig: syscall.SIGTERM})
	var rel *attach.ReleaseError
	if err := w.app.Close(); !errors.As(err, &rel) || rel != failed {
		t.Errorf("Close after a SIGTERM whose release failed = %v, want that failure", err)
	}

	// The screens quit with a hold taken and not yet attached with, and
	// its release fails.
	w = newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	refuse := func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, "/release") {
			return http.StatusNotFound
		}
		return 0
	}
	w.refuse.Store(&refuse)
	w.send(keyPress("a"))
	w.feedOne()
	if err := w.app.Close(); !errors.As(err, &rel) || !strings.Contains(err.Error(), "expires") {
		t.Errorf("Close with a hold it couldn't give back = %v, want a ReleaseError", err)
	}
	if h := w.srv.ActiveHold(w.task.ID); h == nil {
		t.Error("the release went through: nothing to report")
	}

	// Nothing failed: nothing to report.
	w = newAttachWorld(t, "inline", false)
	w.send(attachedMsg{err: context.Canceled, sig: syscall.SIGTERM})
	if err := w.app.Close(); err != nil {
		t.Errorf("Close after a clean release = %v", err)
	}
}

// The screens quit while tmux was opening the window: a window that
// opened is closed again, as a hold taken is given back.
func TestCloseClosesAWindowNotYetShown(t *testing.T) {
	w := newAttachWorld(t, "auto", true)
	w.send(keyPress("a"))
	w.feedOne() // the task: the window is asked for, its answer waits
	if len(w.windows) != 1 || len(w.closed) != 0 {
		t.Fatalf("windows %d, closed %q; want one open", len(w.windows), w.closed)
	}
	w.app.Close()
	if !slices.Equal(w.closed, []string{"@7"}) {
		t.Errorf("windows closed = %q, want the one that opened", w.closed)
	}
	// A window the list has shown is the person's: Close leaves it.
	w = newAttachWorld(t, "auto", true)
	w.key("a")
	w.app.Close()
	if len(w.closed) != 0 {
		t.Errorf("Close closed a window the list had shown: %q", w.closed)
	}
}

// Commands that don't fit the screen can be scrolled to: none is out of
// reach on a small terminal.
func TestAttachTextScrolls(t *testing.T) {
	h := loaded(t, 60, 15)
	var cmds []attach.Command
	for _, via := range []string{"kubectl", "docker", "ssh", "last"} {
		cmds = append(cmds, attach.Command{Via: via, Argv: []string{via, "exec", "-it", "a-machine-with-a-long-name-0", "--", "tmux", "-L", "loomux", "attach", "-t", "loomux-task-1"}})
	}
	h.app.showAttachError(&attach.UnsupportedError{Plan: attach.Plan{Commands: cmds}})
	if f := h.frame(); !strings.Contains(f, "can't attach yet") || strings.Contains(f, "last exec") {
		t.Fatalf("the top of the text should show, and its end not yet:\n%s", f)
	}
	lines := h.lines()
	if foot := lines[len(lines)-1]; !strings.Contains(foot, "more") || !strings.Contains(foot, "j") {
		t.Errorf("the footer doesn't say there is more and how to get to it: %q", foot)
	}
	for _, l := range lines {
		if n := len([]rune(l)); n > 60 {
			t.Errorf("a line is %d wide on a 60-column screen: %q", n, l)
		}
	}
	if len(lines) != 15 {
		t.Errorf("the frame is %d lines, want 15", len(lines))
	}

	// Down to the end: the last command, whole.
	for i := 0; i < 40; i++ {
		h.key("j")
	}
	f := h.frame()
	if !strings.Contains(f, "last exec") || !strings.Contains(strings.Join(strings.Fields(f), ""), "attach-tloomux-task-1") {
		t.Fatalf("the end of the text isn't reachable:\n%s", f)
	}
	if strings.Contains(f, "can't attach yet") {
		t.Errorf("the text didn't scroll:\n%s", f)
	}
	// And back up, by the other keys too.
	h.key("g")
	if f := h.frame(); !strings.Contains(f, "can't attach yet") {
		t.Errorf("the top isn't back:\n%s", f)
	}
	h.key("G")
	if f := h.frame(); !strings.Contains(f, "last exec") {
		t.Errorf("the bottom isn't reached:\n%s", f)
	}
	h.key("k", "pgup", "pgdown")
	// Any other key closes it.
	h.key("esc")
	if f := h.frame(); strings.Contains(f, "attach by hand") {
		t.Errorf("the text is still up:\n%s", f)
	}

	// Text that fits has nothing to scroll: every key closes it.
	h = loaded(t, 80, 24)
	h.app.showAttachError(&attach.UnsupportedError{Plan: attach.Plan{Commands: cmds[:1]}})
	h.key("j")
	if f := h.frame(); strings.Contains(f, "can't attach yet") {
		t.Errorf("j didn't close a text that fits:\n%s", f)
	}
}

// A signal ended the attach. Told to stop (SIGTERM) or with its terminal
// gone (SIGHUP), the client quits rather than come back; an interrupt
// (ctrl+c at ssh's password prompt) only ends the attach, and the list is
// back.
func TestAttachEndedByASignal(t *testing.T) {
	quits := func(w *attachWorld) bool {
		return slices.ContainsFunc(w.out2(), func(m tea.Msg) bool { _, ok := m.(tea.QuitMsg); return ok })
	}
	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		w := newAttachWorld(t, "inline", false)
		w.send(attachedMsg{err: context.Canceled, sig: sig})
		if !quits(w) {
			t.Errorf("%v: the screens didn't quit: %v", sig, w.out2())
		}
	}
	w := newAttachWorld(t, "inline", false)
	w.send(attachedMsg{err: context.Canceled, sig: os.Interrupt})
	if quits(w) {
		t.Error("an interrupt quit the screens")
	}
	w.flush()
	if f := w.frame(); !strings.Contains(w.footer(), "interrupted") || !strings.Contains(f, "fix the build") {
		t.Errorf("after an interrupt:\n%s", f)
	}
}

// An interrupt ends the attach, whose release then fails: the footer says
// the hold is still held until its lease runs out, not that it was
// released.
func TestAttachInterruptedReportsAFailedRelease(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("started") + "; exec sleep 60")
	refuse := func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, "/release") {
			return http.StatusNotFound
		}
		return 0
	}
	w.refuse.Store(&refuse)
	sent := make(chan error, 1)
	go func() {
		for !w.ran("started") {
			time.Sleep(5 * time.Millisecond)
		}
		sent <- syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()
	w.key("a")
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if h := w.srv.ActiveHold(w.task.ID); h == nil {
		t.Fatal("the release went through: nothing to report")
	}
	f := w.footer()
	if strings.Contains(f, "released") || !strings.Contains(f, "couldn't release") {
		t.Errorf("footer = %q, want the failed release", f)
	}
	if !strings.Contains(f, "interrupted") {
		t.Errorf("footer = %q, want it to say the attach was interrupted", f)
	}

	// The hold ended another way meanwhile: that is said, not a release.
	w = newAttachWorld(t, "inline", false)
	w.send(attachedMsg{err: &attach.EndedError{Reason: "taken_back"}, sig: os.Interrupt})
	if f := w.footer(); strings.Contains(f, "released") || !strings.Contains(f, "taken back") {
		t.Errorf("footer = %q, want how the hold ended", f)
	}
}

// Without an attach set up, a says so.
func TestAttachNotSetUp(t *testing.T) {
	h := loaded(t, 80, 24)
	h.key("a")
	if f := h.frame(); !strings.Contains(f, "attaching isn't set up") {
		t.Errorf("frame:\n%s", f)
	}
}

// The question and the command text as frames.
func TestAttachGoldenFrames(t *testing.T) {
	h := loaded(t, 80, 24)
	h.app.askHeld(&starting{task: "task-1", name: "hub"}, &client.Hold{ClientKind: "tui", ClientLabel: "desk",
		AcquiredAt: time.Date(2026, 11, 20, 14, 2, 0, 0, time.Local)})
	golden(t, "attach-held-80x24", h.frame())

	h = loaded(t, 80, 24)
	h.app.showAttachError(&attach.UnsupportedError{Plan: attach.Plan{Commands: []attach.Command{
		{Via: "kubectl", Argv: []string{"kubectl", "-n", "loomux", "exec", "-it", "hub-0", "--", "tmux", "-L", "loomux", "attach", "-t", "loomux-task-1"}},
		{Via: "ssh", Argv: []string{"ssh", "loomux@hub.example.net", "-t", "tmux -L loomux attach -t loomux-task-1"}},
	}}})
	golden(t, "attach-text-80x24", h.frame())
}

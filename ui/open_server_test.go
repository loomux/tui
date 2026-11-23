package ui

// Opening a conversation in a real Bubble Tea program (keys read as
// bytes, commands run by its loop), with the real client, against a real
// api.Server from the pinned server module (api/apitest): the
// conversation's stream is the server's, and closes with the screen.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/ui/conversation"
)

// watched is the app with its last frame readable from the test.
type watched struct {
	*App
	mu    sync.Mutex
	frame string
}

func (w *watched) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := w.App.Update(msg)
	return w, cmd
}

func (w *watched) View() tea.View {
	v := w.App.View()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frame = ansi.Strip(v.Content)
	return v
}

func (w *watched) shown() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.frame
}

// liveApp is the app in a program, on a test server's front that counts
// the streams open on it.
type liveApp struct {
	t       *testing.T
	srv     *apitest.Server
	api     *client.Client
	app     *watched
	program *tea.Program
	keys    io.Writer
	done    chan error
	streams atomic.Int32
	refuse  atomic.Bool // answer the list's polls 401
}

func newLiveApp(t *testing.T) *liveApp {
	t.Helper()
	r := &liveApp{t: t, srv: apitest.New(t), done: make(chan error, 1)}
	front := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if r.refuse.Load() && strings.HasSuffix(req.URL.Path, "/conversations") {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusUnauthorized)
			_, _ = rw.Write([]byte(`{"error":"unauthorized","code":"unauthorized"}`))
			return
		}
		if strings.HasSuffix(req.URL.Path, "/stream") {
			r.streams.Add(1)
			defer r.streams.Add(-1)
		}
		r.srv.API.ServeHTTP(rw, req)
	}))
	t.Cleanup(func() {
		// A stream a failed test left open would keep Close waiting.
		front.CloseClientConnections()
		front.Close()
	})
	var err error
	if r.api, err = client.New(front.URL, r.srv.SeedLinkedSession("laptop", registry.SessionKindTUI)); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *liveApp) start(o Options) {
	r.t.Helper()
	o.Profile, o.API = "test", r.api
	r.app = &watched{App: New(o)}
	// The list polls at the start and on Refresh only, so that what a
	// test sees is the conversation's doing and not the next poll's.
	r.app.tick = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	in, keys := io.Pipe()
	r.keys = keys
	r.program = tea.NewProgram(r.app, tea.WithInput(in), tea.WithOutput(&bytes.Buffer{}), tea.WithWindowSize(100, 24), tea.WithoutSignals())
	go func() {
		_, err := r.program.Run()
		r.done <- err
	}()
	r.t.Cleanup(func() {
		r.program.Kill()
		keys.Close()
	})
}

func (r *liveApp) press(keys string) {
	r.t.Helper()
	if _, err := io.WriteString(r.keys, keys); err != nil {
		r.t.Fatal(err)
	}
}

func (r *liveApp) until(what string, ok func() bool) {
	r.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			r.t.Fatalf("never %s (%d streams open):\n%s", what, r.streams.Load(), r.app.shown())
		}
	}
}

// wait waits until the screen shows every one of texts.
func (r *liveApp) wait(texts ...string) {
	r.t.Helper()
	r.until("showed "+strings.Join(texts, ", "), func() bool {
		s := r.app.shown()
		for _, text := range texts {
			if !strings.Contains(s, text) {
				return false
			}
		}
		return true
	})
}

// signedOutList waits for the list's signed-out screen. A conversation's
// has the same words, and the messages too.
func (r *liveApp) signedOutList() {
	r.t.Helper()
	r.until("showed the list signed out", func() bool {
		s := r.app.shown()
		return strings.Contains(s, "⚠ signed out") && strings.Contains(s, "Sign in again: loomux login") && !strings.Contains(s, "Done.")
	})
}

func (r *liveApp) noStream() {
	r.t.Helper()
	r.until("closed the conversation's stream", func() bool { return r.streams.Load() == 0 })
}

// enter opens the row's conversation, live; back closes its stream and
// shows the list again.
func TestOpenAgainstTheServer(t *testing.T) {
	r := newLiveApp(t)
	task := r.srv.SeedConversation("", "fix the flaky test", "I fixed **two** things.")
	r.start(Options{})
	r.wait("NEEDS YOU", "fix the flaky test")
	r.press("\r")
	r.wait("loomux · test · apitest", "● live", "I fixed two things.")
	// A change on the server reaches the screen by the stream.
	r.srv.SetTaskStatus(task.ID, registry.TaskStatusNeedsAttention, &registry.Attention{Kind: "permission", Title: "Bash command", Question: "Do you want to proceed?"})
	r.wait("▲ needs attention", "Bash command")
	if n := r.streams.Load(); n != 1 {
		t.Errorf("%d streams open, want 1", n)
	}
	r.press("\x1b")
	r.wait("NEEDS YOU", "updated")
	r.noStream()
	r.press("\r")
	r.wait("● live", "I fixed two things.")
	r.press("q")
	select {
	case err := <-r.done:
		if err != nil {
			t.Errorf("the program ended with %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("q in a conversation didn't quit")
	}
}

// The root closes the conversation it is asked to leave: the screen's
// own closing on esc isn't what it counts on.
func TestBackClosesTheConversation(t *testing.T) {
	r := newLiveApp(t)
	task := r.srv.SeedConversation("", "fix the flaky test", "Done.")
	r.start(Options{Open: task.ConversationID})
	r.wait("● live", "Done.")
	r.program.Send(conversation.BackMsg{})
	r.wait("NEEDS YOU", "updated")
	r.noStream()
}

// loomux open's conversation ends with its session: the 401 its stream
// gets is the list's, with its screen.
func TestOpenSignedOutAgainstTheServer(t *testing.T) {
	r := newLiveApp(t)
	task := r.srv.SeedConversation("", "fix the flaky test", "Done.")
	r.start(Options{Open: task.ConversationID})
	r.wait("● live", "Done.")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := r.api.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	r.signedOutList()
	r.noStream()
}

// The list's own 401 behind a conversation closes it, stream and all.
func TestListSignedOutClosesTheStream(t *testing.T) {
	r := newLiveApp(t)
	task := r.srv.SeedConversation("", "fix the flaky test", "Done.")
	r.start(Options{Open: task.ConversationID})
	r.wait("● live", "Done.")
	r.refuse.Store(true)
	r.program.Send(Refresh())
	r.signedOutList()
	r.noStream()
}

// However the program ends, Close leaves no stream open.
func TestCloseEndsTheConversation(t *testing.T) {
	r := newLiveApp(t)
	task := r.srv.SeedConversation("", "fix the flaky test", "Done.")
	r.start(Options{Open: task.ConversationID})
	r.wait("● live", "Done.")
	r.program.Kill()
	<-r.done
	if err := r.app.Close(); err != nil {
		t.Error(err)
	}
	r.noStream()
}

package conversation_test

// The screen as a root model would run it: in a real Bubble Tea program
// (keys read as bytes from its input, its commands run by its loop),
// with the real client, against a real api.Server from the pinned
// server module (api/apitest). The other tests feed the screen its
// messages by hand; this one checks that the pieces meet.

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/ui/conversation"
)

// root is the least a root model does with the screen: it uses only what
// the package exports.
type root struct {
	screen *conversation.Model

	mu        sync.Mutex
	view      string
	left      bool
	signedOut error
}

func (r *root) Init() tea.Cmd { return r.screen.Init() }

func (r *root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case conversation.BackMsg:
		r.screen.Close()
		r.mu.Lock()
		r.left = true
		r.mu.Unlock()
		return r, tea.Quit
	case conversation.SignedOutMsg:
		r.mu.Lock()
		r.signedOut = msg.Err
		r.mu.Unlock()
		return r, nil
	}
	_, cmd := r.screen.Update(msg)
	return r, cmd
}

func (r *root) View() tea.View {
	s := r.screen.View()
	r.mu.Lock()
	r.view = ansi.Strip(s)
	r.mu.Unlock()
	v := tea.NewView(s)
	v.AltScreen = true
	return v
}

func (r *root) shown() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.view
}

// program is a running program and the pipe its keys are typed into.
type program struct {
	t    *testing.T
	root *root
	keys io.Writer
	done chan error
}

// run starts the screen of conversation id at 80x24 and stops it when
// the test ends.
func run(t *testing.T, c *client.Client, id string) *program {
	t.Helper()
	r := &root{screen: conversation.New(conversation.Options{Client: c, ID: id, Profile: "test", Title: "apitest", Width: 80, Height: 24})}
	in, keys := io.Pipe()
	p := tea.NewProgram(r, tea.WithInput(in), tea.WithOutput(&bytes.Buffer{}), tea.WithWindowSize(80, 24), tea.WithoutSignals())
	pr := &program{t: t, root: r, keys: keys, done: make(chan error, 1)}
	go func() {
		_, err := p.Run()
		pr.done <- err
	}()
	t.Cleanup(func() {
		p.Kill()
		keys.Close()
		r.screen.Close()
	})
	return pr
}

// wait waits until the screen shows every one of texts and none of
// gone.
func (p *program) wait(texts []string, gone ...string) {
	p.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		s := p.root.shown()
		ok := true
		for _, text := range texts {
			ok = ok && strings.Contains(s, text)
		}
		for _, text := range gone {
			ok = ok && !strings.Contains(s, text)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("the screen never showed %q without %q:\n%s", texts, gone, s)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (p *program) typeKeys(keys string) {
	p.t.Helper()
	if _, err := io.WriteString(p.keys, keys); err != nil {
		p.t.Fatal(err)
	}
}

func signedIn(t *testing.T, srv *apitest.Server) *client.Client {
	t.Helper()
	c, err := client.New(srv.URL, srv.SeedLinkedSession("laptop", registry.SessionKindTUI))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestScreenAgainstARealServer(t *testing.T) {
	srv := apitest.New(t)
	c := signedIn(t, srv)
	task := srv.SeedConversation("", "fix the flaky test", "I fixed **two** things.")
	srv.SeedTurn(task, "fix the flaky test", "Fixed.", "$ go test ./...\nok")
	p := run(t, c, task.ConversationID)

	// The conversation, fetched, and the stream, connected.
	p.wait([]string{"loomux · test · apitest", "● live", "fix the flaky test", "I fixed two things.", "awaiting input"}, "**two**", "loading")

	// A message sent from another client: its turn in flight, then the
	// reply, as the stream reports them.
	release := srv.Dispatcher.Block()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := c.Send(ctx, client.SendRequest{ConversationID: task.ConversationID, Message: "and the nightly?"}); err != nil {
		t.Fatal(err)
	}
	p.wait([]string{"and the nightly?", "◐ working"})
	release()
	p.wait([]string{"and the nightly?", "loomux ·", "ok"}, "◐ working", "◐ queued")
	if n := strings.Count(p.root.shown(), "and the nightly?"); n != 1 {
		t.Errorf("the message shows %d times:\n%s", n, p.root.shown())
	}

	// A task stopping at a prompt.
	srv.SetTaskStatus(task.ID, registry.TaskStatusNeedsAttention, &registry.Attention{Kind: "permission", Title: "Bash command", Question: "Do you want to proceed?"})
	p.wait([]string{"▲ needs attention", "Bash command"}, "awaiting input")

	// The keys, typed: the task's transcript, the help over it, back to
	// the transcript, back to the conversation.
	p.typeKeys("p")
	p.wait([]string{"transcript of", "$ go test ./...", "Fixed."}, "p transcript")
	p.typeKeys("?")
	p.wait([]string{"Keys in a transcript"}, "$ go test ./...")
	p.typeKeys("?")
	p.wait([]string{"$ go test ./..."}, "Keys in a transcript")
	p.typeKeys("\x1b")
	p.wait([]string{"p transcript", "I fixed two things."}, "$ go test ./...")

	// Back leaves the screen: the root is told, and quits here.
	p.typeKeys("\x1b")
	select {
	case err := <-p.done:
		if err != nil {
			t.Errorf("the program ended with %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("back didn't leave the screen")
	}
	p.root.mu.Lock()
	defer p.root.mu.Unlock()
	if !p.root.left || p.root.signedOut != nil {
		t.Errorf("left = %v, signedOut = %v, want the root told BackMsg only", p.root.left, p.root.signedOut)
	}
}

// Logging out elsewhere ends the session: its stream ends, the reconnect
// is refused with the 401, and the root hears SignedOutMsg.
func TestScreenIsSignedOutWhenItsSessionEnds(t *testing.T) {
	srv := apitest.New(t)
	c := signedIn(t, srv)
	task := srv.SeedConversation("", "fix the flaky test", "Done.")
	p := run(t, c, task.ConversationID)
	p.wait([]string{"● live", "Done."})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	p.wait([]string{"signed out", "loomux login", "Done."})
	p.root.mu.Lock()
	defer p.root.mu.Unlock()
	if !client.Unauthorized(p.root.signedOut) {
		t.Errorf("the root heard %v, want SignedOutMsg with the 401", p.root.signedOut)
	}
}

// An older page, against the server's own paging: more messages than one
// fetch asks for, and the first of them reached by scrolling up.
func TestScreenPagesBackThroughARealConversation(t *testing.T) {
	srv := apitest.New(t)
	c := signedIn(t, srv)
	task := srv.SeedConversation("", "the very first message", "the first reply")
	for i := 0; i < 60; i++ {
		srv.SeedMessage(task.ConversationID, registry.MessageRoleAssistant, "a later message", task)
	}
	p := run(t, c, task.ConversationID)
	p.wait([]string{"● live", "a later message"}, "the very first message")

	// g goes to the top of what is loaded and asks for the page before
	// it; once that has arrived, g again reaches the first message.
	p.typeKeys("g")
	p.wait([]string{"↓ more below"}, "scroll up for older messages", "loading older messages")
	p.typeKeys("g")
	p.wait([]string{"the very first message", "the first reply"})
}

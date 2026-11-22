package ui

// The list's handoff against a real api.Server from the pinned server
// module (api/apitest): the calls a take back makes, as the server
// answers them. The pinned server has holds but doesn't expose them on
// rows or tasks yet (S6), so this is the take back a server without S6
// gets.

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"

	"github.com/Loomux/tui/client"
)

func serverClient(t *testing.T, srv *apitest.Server, label string) *client.Client {
	t.Helper()
	c, err := client.New(srv.URL, srv.SeedLinkedSession(label, registry.SessionKindTUI))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// loomux open on a conversation another terminal drives, then T: the
// list starts on it, and the take back ends the other terminal's hold
// and hands the task back.
func TestTakeBackAgainstTheServer(t *testing.T) {
	srv := apitest.New(t)
	other := srv.SeedConversation("", "a quick question", "an answer")
	task := srv.SeedConversation("", "triage the inbox", "on it")
	desk := serverClient(t, srv, "desk")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := desk.Takeover(ctx, task.ID, client.TakeoverRequest{ClientLabel: "desk"}); err != nil {
		t.Fatal(err)
	}

	h := &harness{t: t, now: time.Now()}
	h.app = New(Options{Profile: "test", API: serverClient(t, srv, "laptop"), Server: srv.URL, Open: task.ConversationID,
		Browse: func(link string) error { h.browsed = append(h.browsed, link); return nil }})
	h.app.tick = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return firedTick{d, fn(time.Now().Add(d))} }
	}
	h.run(h.app.Init())
	h.send(tea.WindowSizeMsg{Width: 100, Height: 24})
	h.flush()

	if h.selected() != task.ConversationID {
		t.Fatalf("on %q, want %q (open); other is %q", h.selected(), task.ConversationID, other.ConversationID)
	}
	if f := h.footer(); !strings.Contains(f, "opening a conversation") {
		t.Errorf("footer after open: %q, want enter's notice", f)
	}
	h.key("esc") // the notice lasts until a key
	if f := h.footer(); !strings.Contains(f, "T take back") {
		t.Errorf("footer on the held row: %q", f)
	}
	h.key("T")
	if f := h.footer(); !strings.Contains(f, "took it back") || !strings.Contains(f, "awaiting input") {
		t.Errorf("footer after T: %q", f)
	}
	if hold := srv.ActiveHold(task.ID); hold != nil {
		t.Errorf("still held after the take back: %+v", hold)
	}
	if it, _ := h.app.home.current(); it.Status != client.TaskAwaitingInput {
		t.Errorf("the row after the take back's poll: %s, want awaiting_input", it.Status)
	}

	h.key("o")
	if want := srv.URL + "/conversations/" + task.ConversationID; len(h.browsed) != 1 || h.browsed[0] != want {
		t.Errorf("browsed %q, want %s", h.browsed, want)
	}
}

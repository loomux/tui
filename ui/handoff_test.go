package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
	"github.com/Loomux/tui/internal/browser"
)

func (f *fakeAPI) Conversation(_ context.Context, id string, page client.Page) (*client.Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.convAsked = append(f.convAsked, id)
	if f.convErr != nil {
		return nil, f.convErr
	}
	c, ok := f.convs[id]
	if !ok {
		return nil, &client.Error{Status: 404, Code: client.CodeNotFound, Message: "conversation not found"}
	}
	cp := *c
	cp.Tasks = slices.Clone(c.Tasks)
	return &cp, nil
}

func (f *fakeAPI) Release(_ context.Context, taskID, holdID string) (*client.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, taskID)
	f.releasedAs = append(f.releasedAs, holdID)
	if f.releaseErr != nil {
		return nil, f.releaseErr
	}
	r := &client.Release{TaskID: taskID, Status: client.TaskAwaitingInput}
	for _, c := range f.convs {
		for _, tk := range c.Tasks {
			if tk.ID == taskID && tk.Hold != nil {
				h := *tk.Hold
				h.EndedReason = "taken_back"
				r.Hold = &h
			}
		}
	}
	if taskID == "t-failed" {
		r.Status = client.TaskFailed
	}
	return r, nil
}

const server = "https://loomux.example.net"

// handoff is the sample list on a server, with the browser recorded.
func handoff(t *testing.T, api *fakeAPI, o Options) *harness {
	t.Helper()
	if api.page == nil {
		api.page = samplePage()
	}
	api.workspaces = sampleWorkspaces()
	if o.Server == "" {
		o.Server = server
	}
	h := newHarnessOpts(t, api, 100, 24, o)
	h.flush()
	return h
}

// to moves the cursor onto a conversation.
func (h *harness) to(id string) {
	h.t.Helper()
	for range len(h.app.home.visible) {
		if h.selected() == id {
			return
		}
		h.key("j")
	}
	h.t.Fatalf("%s isn't in the list", id)
}

func (h *harness) footer() string {
	l := h.lines()
	return l[len(l)-1]
}

// o opens the conversation under the cursor on the web:
// <server>/conversations/<id>, the id escaped.
func TestBrowserOpensTheConversation(t *testing.T) {
	page := samplePage()
	page.Conversations = append(page.Conversations, client.ConversationSummary{
		ConversationID: "a b/c?d", Status: client.TaskRunning, UpdatedAt: t0, Preview: "odd id"})
	h := handoff(t, &fakeAPI{page: page}, Options{Server: server})
	h.key("o")
	want := server + "/conversations/conv-attn"
	if !slices.Equal(h.browsed, []string{want}) {
		t.Fatalf("browsed %q, want %q", h.browsed, want)
	}
	if f := h.footer(); !strings.Contains(f, "opened") || !strings.Contains(f, want) {
		t.Errorf("footer %q, want it to say it opened %s", f, want)
	}
	h.to("a b/c?d")
	h.key("o")
	if got := h.browsed[len(h.browsed)-1]; got != server+"/conversations/a%20b%2Fc%3Fd" {
		t.Errorf("browsed %q: the id isn't one escaped path segment", got)
	}
}

// Where no browser can be opened (over ssh, say) the link is shown to
// copy; another failure says why, with the link too.
func TestBrowserUnavailableShowsTheLink(t *testing.T) {
	want := server + "/conversations/conv-attn"
	for _, c := range []struct {
		err   error
		words []string
	}{
		{browser.ErrNoBrowser, []string{want, "no browser"}},
		{errors.New("exec: xdg-open: not found"), []string{want, "couldn't open a browser"}},
	} {
		h := handoff(t, &fakeAPI{}, Options{Browse: func(string) error { return c.err }})
		h.key("o")
		f := h.footer()
		for _, w := range c.words {
			if !strings.Contains(f, w) {
				t.Errorf("%v: footer %q lacks %q", c.err, f, w)
			}
		}
		// The link comes first, so a narrow footer cuts the words, not it.
		if !strings.HasPrefix(strings.TrimSpace(f), want) {
			t.Errorf("%v: footer %q doesn't start with the link", c.err, f)
		}
	}
}

// With no row under the cursor there is nothing to open.
func TestBrowserWithNoRow(t *testing.T) {
	h := handoff(t, &fakeAPI{page: &client.ConversationPage{}}, Options{})
	h.key("o")
	if len(h.browsed) != 0 {
		t.Errorf("browsed %q with no row", h.browsed)
	}
}

// heldConv is conv-held's conversation: an ended task and the held one.
func heldConv(held client.Task) map[string]*client.Conversation {
	return map[string]*client.Conversation{"conv-held": {ConversationID: "conv-held", Tasks: []client.Task{
		{ID: "t-old", Status: client.TaskCompleted},
		held,
	}}}
}

func driveHold(task, label string) *client.Hold {
	return &client.Hold{ID: "h-" + task, TaskID: task, Mode: client.ModeDrive, ClientKind: "tui", ClientLabel: label,
		AcquiredAt: t0.Add(-time.Hour)}
}

// T on a held row takes the conversation back: the held task is
// released without a hold id (the API's Take back), the list says
// from whom and how the task is now, and polls at once.
func TestTakeBack(t *testing.T) {
	api := &fakeAPI{convs: heldConv(client.Task{ID: "t-held", Status: client.TaskHumanTakeover, Hold: driveHold("t-held", "laptop")})}
	h := handoff(t, api, Options{})
	polls, _ := api.calls()
	h.to("conv-held")
	h.key("T")
	if !slices.Equal(api.convAsked, []string{"conv-held"}) {
		t.Errorf("conversations asked: %q", api.convAsked)
	}
	if !slices.Equal(api.released, []string{"t-held"}) || !slices.Equal(api.releasedAs, []string{""}) {
		t.Errorf("released %q with hold ids %q, want t-held with none (Take back)", api.released, api.releasedAs)
	}
	if f := h.footer(); !strings.Contains(f, "took it back from laptop") || !strings.Contains(f, "awaiting input") {
		t.Errorf("footer %q", f)
	}
	if n, _ := api.calls(); n != polls+1 {
		t.Errorf("%d polls after the take back, want one more than %d", n, polls)
	}
}

// The release names the hold it ended: if another terminal took the
// task since the last poll, the notice names that one, not the row's.
func TestTakeBackNamesTheHoldItEnded(t *testing.T) {
	api := &fakeAPI{convs: heldConv(client.Task{ID: "t-held", Status: client.TaskHumanTakeover, Hold: driveHold("t-held", "desk")})}
	h := handoff(t, api, Options{})
	h.to("conv-held") // the row still says laptop
	h.key("T")
	if f := h.footer(); !strings.Contains(f, "took it back from desk") {
		t.Errorf("footer %q", f)
	}
}

// A failed task kept for a person (signing in, say) can be held, and
// stays failed: its hold, not its status, says it's held (S6).
func TestTakeBackAHeldFailedTask(t *testing.T) {
	api := &fakeAPI{convs: heldConv(client.Task{ID: "t-failed", Status: client.TaskFailed, ErrorClass: "login_required",
		Hold: driveHold("t-failed", "laptop")})}
	h := handoff(t, api, Options{})
	h.to("conv-held")
	h.key("T")
	if !slices.Equal(api.released, []string{"t-failed"}) {
		t.Errorf("released %q, want t-failed", api.released)
	}
	if f := h.footer(); !strings.Contains(f, "took it back from laptop") || !strings.Contains(f, "failed") {
		t.Errorf("footer %q", f)
	}
}

// A server without S6 sends no holds: a task in human_takeover is the
// held one, and a row in human_takeover without held_by can be taken
// back.
func TestTakeBackWithoutHolds(t *testing.T) {
	page := samplePage()
	for i := range page.Conversations {
		page.Conversations[i].HeldBy = nil
	}
	api := &fakeAPI{page: page, convs: heldConv(client.Task{ID: "t-held", Status: client.TaskHumanTakeover})}
	h := handoff(t, api, Options{})
	h.to("conv-held")
	h.key("T")
	if !slices.Equal(api.released, []string{"t-held"}) {
		t.Errorf("released %q, want t-held", api.released)
	}
	if f := h.footer(); !strings.Contains(f, "took it back") || !strings.Contains(f, "awaiting input") {
		t.Errorf("footer %q", f)
	}
}

// A watch hold isn't taken back: Take back ends drive holds only.
func TestTakeBackLeavesWatchers(t *testing.T) {
	watch := driveHold("t-run", "desk")
	watch.Mode = client.ModeWatch
	api := &fakeAPI{convs: heldConv(client.Task{ID: "t-run", Status: client.TaskRunning, Hold: watch})}
	h := handoff(t, api, Options{})
	h.to("conv-held")
	h.key("T")
	if len(api.released) != 0 {
		t.Errorf("released %q: a watch was taken back", api.released)
	}
	if f := h.footer(); !strings.Contains(f, "nothing to take back") {
		t.Errorf("footer %q", f)
	}
}

// A row nobody drives has nothing to take back, and nothing is asked.
func TestTakeBackNotHeld(t *testing.T) {
	api := &fakeAPI{}
	h := handoff(t, api, Options{})
	h.key("T") // conv-attn: needs attention
	if len(api.convAsked) != 0 || len(api.released) != 0 {
		t.Errorf("asked %q, released %q for a row nobody drives", api.convAsked, api.released)
	}
	if f := h.footer(); !strings.Contains(f, "isn't driven from a terminal") {
		t.Errorf("footer %q", f)
	}
}

// The hold may have ended since the last poll: nothing to take back, and
// the list polls to catch up.
func TestTakeBackAlreadyGiven(t *testing.T) {
	api := &fakeAPI{convs: heldConv(client.Task{ID: "t-held", Status: client.TaskAwaitingInput})}
	h := handoff(t, api, Options{})
	polls, _ := api.calls()
	h.to("conv-held")
	h.key("T")
	if len(api.released) != 0 {
		t.Errorf("released %q", api.released)
	}
	if f := h.footer(); !strings.Contains(f, "nothing to take back") {
		t.Errorf("footer %q", f)
	}
	if n, _ := api.calls(); n != polls+1 {
		t.Errorf("%d polls, want %d", n, polls+1)
	}
}

// The server's refusal is shown as it said it: detach_failed means the
// holder's terminal couldn't be detached, and nothing changed.
func TestTakeBackRefused(t *testing.T) {
	for _, c := range []struct {
		name               string
		convErr, relErr    error
		want               string
		released, polledAt bool
	}{
		{"detach failed", nil, &client.Error{Status: 502, Code: client.CodeDetachFailed,
			Message: "the holder's terminal could not be detached; try again"}, "the holder's terminal could not be detached", true, true},
		{"conversation gone", &client.Error{Status: 404, Code: client.CodeNotFound, Message: "conversation not found"}, nil,
			"conversation not found", false, true},
	} {
		api := &fakeAPI{convErr: c.convErr, releaseErr: c.relErr,
			convs: heldConv(client.Task{ID: "t-held", Status: client.TaskHumanTakeover, Hold: driveHold("t-held", "laptop")})}
		h := handoff(t, api, Options{})
		polls, _ := api.calls()
		h.to("conv-held")
		h.key("T")
		f := h.footer()
		if !strings.Contains(f, "couldn't take it back") || !strings.Contains(f, c.want) {
			t.Errorf("%s: footer %q", c.name, f)
		}
		if (len(api.released) > 0) != c.released {
			t.Errorf("%s: released %q", c.name, api.released)
		}
		if n, _ := api.calls(); n != polls+1 {
			t.Errorf("%s: %d polls, want %d", c.name, n, polls+1)
		}
	}
}

// One take back at a time: a second T while the first is answered asks
// nothing more.
func TestTakeBackOnce(t *testing.T) {
	api := &fakeAPI{convs: heldConv(client.Task{ID: "t-held", Status: client.TaskHumanTakeover, Hold: driveHold("t-held", "laptop")})}
	h := handoff(t, api, Options{})
	h.to("conv-held")
	h.send(keyPress("T"))
	h.send(keyPress("T"))
	if f := h.footer(); !strings.Contains(f, "taking it back") {
		t.Errorf("footer %q while the take back is in flight", f)
	}
	h.flush()
	if len(api.convAsked) != 1 || len(api.released) != 1 {
		t.Errorf("asked %q, released %q: want one take back", api.convAsked, api.released)
	}
}

// On a held row the footer leads with who holds it, since when, and the
// key that takes it back; on other rows it doesn't.
func TestHeldRowFooter(t *testing.T) {
	h := handoff(t, &fakeAPI{}, Options{})
	if f := h.footer(); strings.Contains(f, "take back") {
		t.Errorf("footer on an unheld row: %q", f)
	}
	h.to("conv-held")
	f := h.footer()
	if !strings.HasPrefix(strings.TrimSpace(f), "T take back") || !strings.Contains(f, "laptop") || !strings.Contains(f, "since 11:00") {
		t.Errorf("footer on the held row: %q", f)
	}
	// The key is the effective one.
	keys, err := keymap.New(map[string][]string{"take_back": {"ctrl+t"}})
	if err != nil {
		t.Fatal(err)
	}
	h = handoff(t, &fakeAPI{}, Options{Keys: keys})
	h.to("conv-held")
	if f := h.footer(); !strings.Contains(f, "ctrl+t take back") {
		t.Errorf("footer with take_back on ctrl+t: %q", f)
	}
}

// The holder's since is a date too when it isn't today.
func TestHeldSince(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		since time.Time
		want  string
	}{
		{now.Add(-time.Hour), "since 11:00"},
		{now.Add(-25 * time.Hour), "since Nov 19 11:00"},
		{time.Time{}, ""},
	} {
		if got := heldSince(c.since, now); got != c.want {
			t.Errorf("heldSince(%v) = %q, want %q", c.since, got, c.want)
		}
	}
}

// loomux open <id>: the list starts on that conversation, and opening it
// goes the way enter does.
func TestOpenAtStart(t *testing.T) {
	h := handoff(t, &fakeAPI{}, Options{Open: "conv-done"})
	if h.selected() != "conv-done" {
		t.Errorf("on %q, want conv-done", h.selected())
	}
	if f := h.footer(); !strings.Contains(f, "opening a conversation isn't in this version yet") {
		t.Errorf("footer %q, want enter's notice", f)
	}
	// Later polls leave the cursor where the user puts it.
	h.key("k")
	h.send(refreshMsg{})
	h.flush()
	if h.selected() == "conv-done" {
		t.Error("a later poll moved the cursor back to the opened conversation")
	}
}

// A view or filter chosen before the list came in doesn't hide the
// conversation: the list shows them all to put the cursor on it.
func TestOpenAtStartShowsAll(t *testing.T) {
	api := &fakeAPI{page: samplePage(), workspaces: sampleWorkspaces()}
	h := newHarnessOpts(t, api, 100, 24, Options{Server: server, Open: "conv-done"})
	h.send(keyPress("tab")) // needs you, which conv-done isn't
	h.flush()
	if h.selected() != "conv-done" || h.app.home.view != allView {
		t.Errorf("on %q in view %v, want conv-done in the all view", h.selected(), h.app.home.view)
	}
}

// A conversation the list doesn't have is named, and the cursor stays.
func TestOpenAtStartNotListed(t *testing.T) {
	h := handoff(t, &fakeAPI{}, Options{Open: "conv-nope"})
	if h.selected() != "conv-attn" {
		t.Errorf("on %q", h.selected())
	}
	if f := h.footer(); !strings.Contains(f, `"conv-nope" isn't in the list`) {
		t.Errorf("footer %q", f)
	}
}

// A first poll that fails doesn't use up the open: the first list does.
func TestOpenAtStartAfterAnError(t *testing.T) {
	api := &fakeAPI{page: samplePage(), err: errors.New("connection refused")}
	h := handoff(t, api, Options{Open: "conv-done"})
	api.mu.Lock()
	api.err = nil
	api.mu.Unlock()
	h.send(refreshMsg{})
	h.flush()
	if h.selected() != "conv-done" {
		t.Errorf("on %q, want conv-done", h.selected())
	}
}

// enter's notice points at what works meanwhile.
func TestEnterPointsAtTheBrowser(t *testing.T) {
	h := handoff(t, &fakeAPI{}, Options{})
	h.key("enter")
	if f := h.footer(); !strings.Contains(f, "o opens it in the browser") {
		t.Errorf("footer %q", f)
	}
}

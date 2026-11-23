package ui

// Opening a conversation from the list: enter and loomux open show its
// screen over the list, back returns. The fake's streams are closed ones;
// open_server_test.go runs the screens on a real server's.

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
	"github.com/Loomux/tui/ui/conversation"
)

func (f *fakeAPI) Transcript(context.Context, string, client.Page) (*client.Transcript, error) {
	return &client.Transcript{}, nil
}

// OpenStream is a stream already closed: the harness runs every command
// to its end, and an open stream's next event may never come.
func (f *fakeAPI) OpenStream(id string) *client.Stream { return closedStream(id) }

func closedStream(id string) *client.Stream {
	s := (&client.Client{}).OpenStream(id)
	s.Close()
	return s
}

// said is what the sample's conversations say, by id.
func said(id string) string { return "what " + id + " says" }

// openable is the sample list's server, with a message in each
// conversation.
func openable() *fakeAPI {
	api := &fakeAPI{page: samplePage(), workspaces: sampleWorkspaces(), convs: map[string]*client.Conversation{}}
	for _, r := range api.page.Conversations {
		api.convs[r.ConversationID] = &client.Conversation{ConversationID: r.ConversationID,
			Messages: []client.Message{{ID: "m-" + r.ConversationID, Role: client.RoleUser, Content: said(r.ConversationID)}}}
	}
	return api
}

func (f *fakeAPI) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.convAsked)
}

func (f *fakeAPI) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// wantConversation checks the screen is conversation id's, named title.
func (h *harness) wantConversation(id, title string) {
	h.t.Helper()
	f := h.frame()
	if !strings.Contains(f, said(id)) || strings.Contains(f, "NEEDS YOU") {
		h.t.Fatalf("%s isn't shown:\n%s", id, f)
	}
	if l := h.lines()[0]; !strings.Contains(l, "loomux · test · "+title+" ") {
		h.t.Errorf("header %q, want it to name %q", l, title)
	}
}

// wantList checks the screen is the list.
func (h *harness) wantList() {
	h.t.Helper()
	if f := h.frame(); !strings.Contains(f, "NEEDS YOU") || strings.Contains(f, "what conv-") {
		h.t.Fatalf("the list isn't shown:\n%s", f)
	}
}

// enter opens the conversation under the cursor, over the list.
func TestEnterOpensTheConversation(t *testing.T) {
	h := handoff(t, openable(), Options{})
	h.to("conv-await")
	h.key("enter")
	h.wantConversation("conv-await", "notes")
	if got := h.api.asked(); !slices.Equal(got, []string{"conv-await"}) {
		t.Errorf("fetched %q, want conv-await once", got)
	}
}

// With no row under the cursor there is nothing to open.
func TestEnterWithNoRow(t *testing.T) {
	h := handoff(t, &fakeAPI{page: &client.ConversationPage{}}, Options{})
	h.key("enter")
	if got := h.api.asked(); len(got) != 0 {
		t.Errorf("fetched %q with no row", got)
	}
	if f := h.frame(); !strings.Contains(f, "no conversations yet") {
		t.Errorf("the list isn't shown:\n%s", f)
	}
}

// back leaves the conversation: the list is as it was left, and is
// polled at once (the design: right after any action).
func TestBackReturnsToTheList(t *testing.T) {
	h := handoff(t, openable(), Options{})
	h.to("conv-await")
	h.key("enter")
	h.wantConversation("conv-await", "notes")
	polls, _ := h.api.calls()
	h.key("esc")
	h.wantList()
	if h.selected() != "conv-await" {
		t.Errorf("back on %q, want conv-await", h.selected())
	}
	if n, _ := h.api.calls(); n != polls+1 {
		t.Errorf("%d polls after back, want one more than %d", n, polls)
	}
	// The list has the keys again, and another conversation can be opened.
	h.key("j")
	if h.selected() == "conv-await" {
		t.Error("j didn't move the list's cursor")
	}
	id := h.selected()
	h.key("enter")
	if f := h.frame(); !strings.Contains(f, said(id)) {
		t.Errorf("%s isn't shown:\n%s", id, f)
	}
}

// While a conversation is open the keys are its own: the list under it
// doesn't move, and q quits.
func TestConversationHasTheKeys(t *testing.T) {
	h := handoff(t, openable(), Options{})
	at := h.selected()
	h.key("enter", "j", "G", "/", "tab")
	h.wantConversation(at, "cc-hub")
	if h.selected() != at || h.app.screen != keymap.ListScreen || h.app.home.view != allView {
		t.Errorf("the list took the conversation's keys: on %q, screen %s, view %v", h.selected(), h.app.screen, h.app.home.view)
	}
	h.send(keyPress("q"))
	if !slices.ContainsFunc(h.out, func(m tea.Msg) bool { _, ok := m.(tea.QuitMsg); return ok }) {
		t.Errorf("q in a conversation yielded %v, want the program to quit", h.out)
	}
}

// The list goes on polling behind the conversation, and both take the
// window's size.
func TestListGoesOnBehindTheConversation(t *testing.T) {
	h := newHarnessOpts(t, openable(), 100, 24, Options{})
	ticks := h.flush()
	if len(ticks) != 1 {
		t.Fatalf("ticks %+v, want one", ticks)
	}
	h.key("enter")
	polls, _ := h.api.calls()
	h.api.set(func() { h.api.page.Conversations = h.api.page.Conversations[:2] })
	h.send(ticks[0].msg)
	if n, _ := h.api.calls(); n != polls+1 {
		t.Fatalf("%d polls behind the conversation, want %d", n, polls+1)
	}
	if ticks = h.flush(); len(ticks) != 1 {
		t.Errorf("ticks %+v after that poll, want the next one", ticks)
	}
	h.send(tea.WindowSizeMsg{Width: 70, Height: 18})
	h.wantConversation("conv-attn", "cc-hub")
	fits := func(what string) {
		t.Helper()
		if n := len(h.lines()); n != 18 {
			t.Errorf("%s: %d lines, want 18:\n%s", what, n, h.frame())
		}
	}
	fits("the conversation")
	h.key("esc")
	h.wantList()
	fits("the list")
	if f := h.frame(); strings.Contains(f, "summarise") {
		t.Errorf("the list isn't as the poll behind the conversation left it:\n%s", f)
	}
}

// Under 60×15 a conversation asks for more room, as the list does.
func TestConversationTooSmall(t *testing.T) {
	h := handoff(t, openable(), Options{})
	h.key("enter")
	h.send(tea.WindowSizeMsg{Width: 59, Height: 24})
	if f := h.frame(); strings.Contains(f, "\n") || !strings.Contains(f, "60×15") {
		t.Errorf("at 59×24: %q", f)
	}
}

// What an action of the list's has to say when it ends behind the
// conversation is said there, until the next key.
func TestListNoticeOverTheConversation(t *testing.T) {
	api := openable()
	api.convs["conv-held"].Tasks = heldConv(client.Task{ID: "t-held", Status: client.TaskHumanTakeover, Hold: driveHold("t-held", "laptop")})["conv-held"].Tasks
	h := handoff(t, api, Options{})
	h.to("conv-held")
	h.send(keyPress("T"))     // its answer waits in h.out
	h.send(keyPress("enter")) // opens the conversation meanwhile
	h.flush()
	h.wantConversation("conv-held", "cc-hub")
	if f := h.footer(); !strings.Contains(f, "took it back from laptop") {
		t.Errorf("footer %q, want how the take back went", f)
	}
	h.key("j")
	if f := h.footer(); strings.Contains(f, "took it back") || !strings.Contains(f, "esc back") {
		t.Errorf("footer after a key: %q, want the conversation's own", f)
	}
}

// signedOutFrame is the list's screen after its own poll's 401.
func signedOutFrame(t *testing.T) string {
	t.Helper()
	api := openable()
	h := handoff(t, api, Options{})
	api.set(func() { api.err = unauthorized })
	h.send(refreshMsg{})
	h.flush()
	f := h.frame()
	if !strings.Contains(f, "signed out") || !strings.Contains(f, "loomux login") {
		t.Fatalf("the list after a 401:\n%s", f)
	}
	return f
}

var unauthorized = &client.Error{Status: 401, Code: client.CodeUnauthorized, Message: "unauthorized"}

// A 401 to the conversation is the list's 401: the same screen, and no
// more polls.
func TestConversationSignedOut(t *testing.T) {
	api := openable()
	h := newHarnessOpts(t, api, 100, 24, Options{Server: server})
	ticks := h.flush()
	api.set(func() { api.convErr = unauthorized })
	h.key("enter")
	if f, want := h.frame(), signedOutFrame(t); f != want {
		t.Fatalf("after the conversation's 401:\n%s\nwant the list's:\n%s", f, want)
	}
	polls, _ := api.calls()
	h.send(refreshMsg{})
	for _, tick := range ticks {
		h.send(tick.msg)
	}
	h.flush()
	if n, _ := api.calls(); n != polls {
		t.Errorf("%d polls once signed out, want none after %d", n, polls)
	}
}

// A poll on its way when the conversation's 401 comes is the last one.
func TestConversationSignedOutDuringAPoll(t *testing.T) {
	api := openable()
	h := handoff(t, api, Options{})
	api.set(func() { api.convErr = unauthorized })
	h.send(keyPress("enter")) // the 401 waits in h.out
	answers := h.out
	h.out = nil
	h.send(refreshMsg{}) // a poll on its way
	h.send(refreshMsg{}) // and another asked for after it
	polled := h.out
	h.out = answers
	h.flush()
	polls, _ := api.calls()
	h.out = polled
	if ticks := h.flush(); len(ticks) != 0 {
		t.Errorf("ticks %+v after the last answer, want none", ticks)
	}
	if n, _ := api.calls(); n != polls {
		t.Errorf("%d polls, want none after %d", n, polls)
	}
	if f := h.frame(); !strings.Contains(f, "loomux login") {
		t.Errorf("the screen isn't signed out:\n%s", f)
	}
}

// The list's own 401 behind a conversation ends it too.
func TestListSignedOutBehindTheConversation(t *testing.T) {
	api := openable()
	h := handoff(t, api, Options{})
	h.key("enter")
	h.wantConversation("conv-attn", "cc-hub")
	api.set(func() { api.err = unauthorized })
	h.send(refreshMsg{})
	h.flush()
	if f, want := h.frame(), signedOutFrame(t); f != want {
		t.Errorf("after the list's 401:\n%s\nwant:\n%s", f, want)
	}
}

// Signed out, the rows aren't shown and enter opens none of them.
func TestEnterSignedOut(t *testing.T) {
	api := openable()
	h := handoff(t, api, Options{})
	api.set(func() { api.err = unauthorized })
	h.send(refreshMsg{})
	h.flush()
	h.key("enter")
	if got := api.asked(); len(got) != 0 {
		t.Errorf("fetched %q with a token the server refused", got)
	}
}

// loomux open <id>: the list starts on that conversation and opens it
// as enter does; back is the list, on it.
func TestOpenAtStart(t *testing.T) {
	h := handoff(t, openable(), Options{Open: "conv-done"})
	h.wantConversation("conv-done", "notes")
	h.key("esc")
	h.wantList()
	if h.selected() != "conv-done" {
		t.Errorf("on %q, want conv-done", h.selected())
	}
	// Later polls neither open it again nor move the cursor back to it.
	h.key("k")
	h.send(refreshMsg{})
	h.flush()
	h.wantList()
	if h.selected() == "conv-done" {
		t.Error("a later poll moved the cursor back to the opened conversation")
	}
}

// The workspaces' names may come after the list: the conversation opened
// at the start is named once they do. (The harness answers the list
// first, so a screen named only when it opens has the id's start.)
func TestOpenAtStartNamesTheWorkspaceLater(t *testing.T) {
	api := openable()
	h := newHarnessOpts(t, api, 100, 24, Options{Open: "conv-done"})
	h.send(h.out[0]) // the page, before the names
	if l := h.lines()[0]; !strings.Contains(l, "loomux · test · "+wsNotes[:8]+" ") {
		t.Errorf("header before the names: %q", l)
	}
	h.out = h.out[1:]
	h.flush()
	h.wantConversation("conv-done", "notes")
}

// The filter or the help opened before the list came in isn't left
// waiting under the conversation: back is the list, with its keys.
func TestOpenAtStartOverTheFilterAndHelp(t *testing.T) {
	for _, k := range []string{"/", "?"} {
		h := newHarnessOpts(t, openable(), 100, 24, Options{Open: "conv-done"})
		h.send(keyPress(k))
		h.flush()
		h.wantConversation("conv-done", "notes")
		h.key("esc")
		h.wantList()
		if h.app.screen != keymap.ListScreen {
			t.Errorf("%s: back is the %s screen, want the list", k, h.app.screen)
		}
		h.key("k")
		if h.selected() == "conv-done" {
			t.Errorf("%s: the list hasn't got the keys", k)
		}
	}
}

// The name is the conversation's own, whatever the cursor is on by then:
// a poll behind it may have taken its row out of the list.
func TestConversationKeepsItsName(t *testing.T) {
	api := openable()
	h := handoff(t, api, Options{})
	h.to("conv-await")
	h.key("enter")
	api.set(func() { api.page.Conversations = api.page.Conversations[:2] })
	h.send(refreshMsg{})
	h.flush()
	h.send(namesMsg{workspaces: sampleWorkspaces()})
	h.wantConversation("conv-await", "notes")
}

// A conversation's 401 that arrives after it was left still signs out,
// whatever is open by then: it is about the token, which every screen
// uses, not about that conversation.
func TestConversationSignedOutLate(t *testing.T) {
	api := openable()
	h := handoff(t, api, Options{})
	h.key("enter")
	h.key("esc", "j", "enter")
	h.wantConversation("conv-offer", "infra-lab")
	h.send(conversation.SignedOutMsg{Err: unauthorized})
	if f, want := h.frame(), signedOutFrame(t); f != want {
		t.Errorf("after a late 401:\n%s\nwant:\n%s", f, want)
	}
	// And what its screen had still to say leaves nothing behind.
	h.send(conversation.BackMsg{})
	if f, want := h.frame(), signedOutFrame(t); f != want {
		t.Errorf("after a late back:\n%s\nwant:\n%s", f, want)
	}
}

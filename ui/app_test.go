package ui

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
)

var update = flag.Bool("update", false, "rewrite the golden frames in testdata/")

// fakeAPI answers the list's calls from fields a test sets.
type fakeAPI struct {
	mu         sync.Mutex
	page       *client.ConversationPage
	err        error
	workspaces []client.Workspace
	wsErr      error
	limits     []int
	wsCalls    int
}

func (f *fakeAPI) Conversations(_ context.Context, limit int) (*client.ConversationPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limits = append(f.limits, limit)
	if f.err != nil {
		return nil, f.err
	}
	cp := *f.page
	cp.Conversations = slices.Clone(f.page.Conversations)
	return &cp, nil
}

func (f *fakeAPI) Workspaces(context.Context) ([]client.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wsCalls++
	return slices.Clone(f.workspaces), f.wsErr
}

func (f *fakeAPI) calls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.limits), f.wsCalls
}

// A tickMsg the test's timer made, with the wait it was asked for.
type firedTick struct {
	wait time.Duration
	msg  tea.Msg
}

// harness drives an App the way Bubble Tea does, one message at a time:
// send runs the command an update returns (its batch flattened) and
// keeps the messages it yields in out, without feeding them back. The
// poll timer doesn't sleep: it yields a firedTick at once.
type harness struct {
	t   *testing.T
	app *App
	api *fakeAPI
	now time.Time
	out []tea.Msg
}

func newHarness(t *testing.T, api *fakeAPI, width, height int) *harness {
	t.Helper()
	return newHarnessKeys(t, api, width, height, keymap.Default())
}

func newHarnessKeys(t *testing.T, api *fakeAPI, width, height int, keys *keymap.Map) *harness {
	t.Helper()
	h := &harness{t: t, api: api, now: t0}
	h.app = New(Options{Profile: "test", API: api, Keys: keys, Now: func() time.Time { return h.now }})
	h.app.tick = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return firedTick{d, fn(h.now.Add(d))} }
	}
	h.run(h.app.Init())
	h.send(tea.WindowSizeMsg{Width: width, Height: height})
	return h
}

func (h *harness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	default:
		h.out = append(h.out, msg)
	}
}

func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	m, cmd := h.app.Update(msg)
	if m != h.app {
		h.t.Fatalf("Update returned another model: %T", m)
	}
	h.run(cmd)
}

// flush feeds the messages commands yielded back in, except the timer's
// ticks, which it returns: a test fires them by sending their msg.
func (h *harness) flush() []firedTick {
	h.t.Helper()
	var ticks []firedTick
	for len(h.out) > 0 {
		msg := h.out[0]
		h.out = h.out[1:]
		if ft, ok := msg.(firedTick); ok {
			ticks = append(ticks, ft)
			continue
		}
		h.send(msg)
	}
	return ticks
}

// key sends a key press as a terminal would.
func (h *harness) key(keys ...string) {
	h.t.Helper()
	for _, k := range keys {
		h.send(keyPress(k))
		h.flush()
	}
}

// typed sends text as typed characters.
func (h *harness) typed(s string) {
	h.t.Helper()
	for _, r := range s {
		h.send(tea.KeyPressMsg{Code: r, Text: string(r)})
		h.flush()
	}
}

func keyPress(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	}
	r := []rune(k)
	if len(r) != 1 {
		panic("keyPress: " + k)
	}
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

// frame is the screen as text, its styles stripped.
func (h *harness) frame() string {
	return ansi.Strip(h.app.View().Content)
}

func (h *harness) lines() []string {
	return strings.Split(h.frame(), "\n")
}

// selected is the conversation the cursor is on.
func (h *harness) selected() string {
	it, ok := h.app.home.current()
	if !ok {
		return ""
	}
	return it.ConversationID
}

const (
	wsHub   = "4f0c6d2e-0000-4000-8000-000000000001"
	wsLab   = "4f0c6d2e-0000-4000-8000-000000000002"
	wsNotes = "4f0c6d2e-0000-4000-8000-000000000003"
	wsWeb   = "4f0c6d2e-0000-4000-8000-000000000004"
	wsGone  = "9a1b2c3d-0000-4000-8000-000000000009"
)

func sampleWorkspaces() []client.Workspace {
	return []client.Workspace{{ID: wsHub, Name: "cc-hub"}, {ID: wsLab, Name: "infra-lab"}, {ID: wsNotes, Name: "notes"}, {ID: wsWeb, Name: "loomux-web"}}
}

// samplePage is a list with every kind of row, in the server's order
// (the most recently updated first).
func samplePage() *client.ConversationPage {
	r := func(id, status, ws, preview string, ago time.Duration) client.ConversationSummary {
		return client.ConversationSummary{ConversationID: id, Status: status, WorkspaceID: ws, Preview: preview, UpdatedAt: t0.Add(-ago)}
	}
	held := r("conv-held", client.TaskHumanTakeover, wsHub, "triage the inbox", time.Hour)
	held.HeldBy = &client.Holder{ClientKind: "tui", ClientLabel: "laptop", Since: t0.Add(-time.Hour)}
	held.Watchers = 1
	offer := r("conv-offer", client.TaskCompleted, wsLab, "Create workspace on sc1?", 9*time.Minute)
	offer.PendingConfirmationID = "conf-1"
	rows := []client.ConversationSummary{
		r("conv-run", client.TaskRunning, wsWeb, "fix the flaky ConversationPage test that times out on CI every other run", 30*time.Second),
		r("conv-attn", client.TaskNeedsAttention, wsHub, "Bash command: kubectl rollout restart deploy/api -n demo", 2*time.Minute),
		offer,
		r("conv-await", client.TaskAwaitingInput, wsNotes, "draft the release notes\nfor 0.4", 25*time.Minute),
		held,
		r("conv-done", client.TaskCompleted, wsNotes, "summarise yesterday's PRs", 3*time.Hour),
		r("conv-failed", client.TaskFailed, wsLab, "upgrade the cluster to 1.31", 26*time.Hour),
		r("conv-direct", client.TaskCompleted, "", "what's the weather in Lisbon?", 50*time.Hour),
		r("conv-gone", client.TaskCompleted, wsGone, "", 9*24*time.Hour),
	}
	return &client.ConversationPage{Conversations: rows}
}

func loaded(t *testing.T, width, height int) *harness {
	t.Helper()
	h := newHarness(t, &fakeAPI{page: samplePage(), workspaces: sampleWorkspaces()}, width, height)
	h.flush()
	return h
}

// On start the list loads the newest 100 conversations and the
// workspaces' names, and shows the conversations attention-first.
func TestStartLoadsTheList(t *testing.T) {
	h := loaded(t, 80, 24)
	if !slices.Equal(h.api.limits, []int{100}) {
		t.Errorf("Conversations limits = %v, want [100]", h.api.limits)
	}
	if _, ws := h.api.calls(); ws != 1 {
		t.Errorf("Workspaces called %d times, want 1", ws)
	}
	f := h.frame()
	for _, w := range []string{"loomux · test", "NEEDS YOU (4)", "RUNNING (1)", "DONE (4)", "cc-hub", "held: laptop", "kubectl rollout"} {
		if !strings.Contains(f, w) {
			t.Errorf("frame lacks %q:\n%s", w, f)
		}
	}
	if i, j := strings.Index(f, "NEEDS YOU"), strings.Index(f, "RUNNING"); i > j {
		t.Errorf("running before needs you:\n%s", f)
	}
	if h.selected() != "conv-attn" {
		t.Errorf("cursor on %q, want the first row", h.selected())
	}
}

// The list polls every 5 s, counted from the last answer, never with two
// polls in flight.
func TestPollsEveryFiveSeconds(t *testing.T) {
	api := &fakeAPI{page: samplePage(), workspaces: sampleWorkspaces()}
	h := newHarness(t, api, 80, 24)
	ticks := h.flush()
	if len(ticks) != 1 || ticks[0].wait != 5*time.Second {
		t.Fatalf("after the first answer: ticks %+v, want one of 5s", ticks)
	}
	api.mu.Lock()
	api.page.Conversations = api.page.Conversations[:1]
	api.mu.Unlock()
	h.now = h.now.Add(5 * time.Second)
	h.send(ticks[0].msg)
	if n, _ := api.calls(); n != 2 {
		t.Fatalf("polls = %d, want 2", n)
	}
	// A second tick while the poll is in flight starts nothing.
	h.send(ticks[0].msg)
	if n, _ := api.calls(); n != 2 {
		t.Errorf("polls = %d with one in flight, want 2", n)
	}
	ticks = h.flush()
	if len(ticks) != 1 || ticks[0].wait != 5*time.Second {
		t.Errorf("after the second answer: ticks %+v, want one of 5s", ticks)
	}
	if strings.Contains(h.frame(), "kubectl") {
		t.Errorf("the second poll's rows aren't shown:\n%s", h.frame())
	}
}

// Refresh fetches at once (the design: "right after any action") and
// restarts the 5 s wait; the old timer's tick then does nothing.
func TestRefreshPollsAtOnce(t *testing.T) {
	api := &fakeAPI{page: samplePage(), workspaces: sampleWorkspaces()}
	h := newHarness(t, api, 80, 24)
	old := h.flush()
	h.send(Refresh())
	if n, _ := api.calls(); n != 2 {
		t.Fatalf("polls = %d after Refresh, want 2", n)
	}
	fresh := h.flush()
	if len(fresh) != 1 {
		t.Fatalf("ticks %+v, want one", fresh)
	}
	h.send(old[0].msg)
	if n, _ := api.calls(); n != 2 {
		t.Errorf("the old timer polled: polls = %d, want 2", n)
	}
	h.send(fresh[0].msg)
	if n, _ := api.calls(); n != 3 {
		t.Errorf("the new timer didn't poll: polls = %d, want 3", n)
	}
	// Refresh while a poll is in flight polls again once it's answered.
	h.send(Refresh())
	if n, _ := api.calls(); n != 3 {
		t.Errorf("polls = %d, want 3 while one is in flight", n)
	}
	h.flush()
	if n, _ := api.calls(); n != 4 {
		t.Errorf("polls = %d, want the refresh's poll after the answer", n)
	}
}

// A failed poll keeps the rows on screen, says what failed and tries
// again in 5 s.
func TestPollErrorKeepsTheRows(t *testing.T) {
	api := &fakeAPI{page: samplePage(), workspaces: sampleWorkspaces()}
	h := newHarness(t, api, 80, 24)
	ticks := h.flush()
	api.mu.Lock()
	api.err = &client.Error{Status: 503, Code: "unavailable", Message: "the server is restarting"}
	api.mu.Unlock()
	h.send(ticks[0].msg)
	ticks = h.flush()
	if len(ticks) != 1 || ticks[0].wait != 5*time.Second {
		t.Errorf("after an error: ticks %+v, want a retry in 5s", ticks)
	}
	f := h.frame()
	if !strings.Contains(f, "kubectl") || !strings.Contains(h.lines()[0], "the server is restarting") {
		t.Errorf("frame after an error:\n%s", f)
	}
	api.mu.Lock()
	api.err = nil
	api.mu.Unlock()
	h.send(ticks[0].msg)
	h.flush()
	if strings.Contains(h.frame(), "restarting") {
		t.Errorf("the error outlived a good poll:\n%s", h.frame())
	}
}

// A 401 ends polling: the token is no good, and asking again won't help.
func TestUnauthorizedStopsPolling(t *testing.T) {
	api := &fakeAPI{err: &client.Error{Status: 401, Code: client.CodeUnauthorized, Message: "unauthorized"}}
	h := newHarness(t, api, 80, 24)
	if ticks := h.flush(); len(ticks) != 0 {
		t.Errorf("ticks %+v after a 401, want none", ticks)
	}
	if f := h.frame(); !strings.Contains(f, "signed out") || !strings.Contains(f, "loomux login") {
		t.Errorf("frame after a 401:\n%s", f)
	}
}

// Before the first answer the list says it's loading; a first poll that
// fails says why in the list's place.
func TestLoadingAndFirstError(t *testing.T) {
	api := &fakeAPI{page: samplePage()}
	h := &harness{t: t, api: api, now: t0}
	h.app = New(Options{Profile: "test", API: api, Keys: keymap.Default(), Now: func() time.Time { return h.now }})
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	if f := h.frame(); !strings.Contains(f, "loading") {
		t.Errorf("before the first answer:\n%s", f)
	}
	h2 := newHarness(t, &fakeAPI{err: errors.New("dial tcp: connection refused")}, 80, 24)
	h2.flush()
	if f := h2.frame(); !strings.Contains(f, "connection refused") {
		t.Errorf("after a failed first poll:\n%s", f)
	}
}

// A workspace the list hasn't a name for gets the names fetched again,
// at most once a minute; until then the start of its id stands in.
func TestWorkspaceNamesRefetched(t *testing.T) {
	api := &fakeAPI{page: samplePage(), workspaces: sampleWorkspaces()}
	h := newHarness(t, api, 80, 24)
	ticks := h.flush()
	if _, ws := api.calls(); ws != 1 {
		t.Fatalf("Workspaces calls = %d, want 1", ws)
	}
	if !strings.Contains(h.frame(), "9a1b2c3d") {
		t.Errorf("an unknown workspace isn't shown by its id:\n%s", h.frame())
	}
	// 5 s on: still unknown, but the names were fetched under a minute ago.
	h.now = h.now.Add(5 * time.Second)
	h.send(ticks[0].msg)
	ticks = h.flush()
	if _, ws := api.calls(); ws != 1 {
		t.Errorf("Workspaces calls = %d within a minute, want 1", ws)
	}
	api.mu.Lock()
	api.workspaces = append(api.workspaces, client.Workspace{ID: wsGone, Name: "new-ws"})
	api.mu.Unlock()
	h.now = h.now.Add(time.Minute)
	h.send(ticks[0].msg)
	h.flush()
	if _, ws := api.calls(); ws != 2 {
		t.Errorf("Workspaces calls = %d a minute on, want 2", ws)
	}
	if !strings.Contains(h.frame(), "new-ws") {
		t.Errorf("the new name isn't shown:\n%s", h.frame())
	}
	// Names failing to load is no error on screen: ids stand in.
	h3 := newHarness(t, &fakeAPI{page: samplePage(), wsErr: errors.New("boom")}, 80, 24)
	h3.flush()
	if f := h3.frame(); strings.Contains(f, "boom") || !strings.Contains(f, "4f0c6d2e") {
		t.Errorf("frame with no names:\n%s", f)
	}
}

// j/k and the arrows move the cursor over rows (never onto a section's
// title), g/G go to the ends, and the page keys move by a screenful.
func TestNavigation(t *testing.T) {
	h := loaded(t, 80, 24)
	order := []string{"conv-attn", "conv-offer", "conv-await", "conv-held", "conv-run", "conv-done", "conv-failed", "conv-direct", "conv-gone"}
	for _, want := range order[1:] {
		h.key("j")
		if h.selected() != want {
			t.Fatalf("j: on %q, want %q", h.selected(), want)
		}
	}
	h.key("j")
	if h.selected() != "conv-gone" {
		t.Errorf("j past the end: on %q", h.selected())
	}
	h.key("k", "up")
	if h.selected() != "conv-failed" {
		t.Errorf("k, up: on %q", h.selected())
	}
	h.key("g")
	if h.selected() != "conv-attn" {
		t.Errorf("g: on %q", h.selected())
	}
	h.key("G")
	if h.selected() != "conv-gone" {
		t.Errorf("G: on %q", h.selected())
	}
	h.key("home", "pgdown")
	if h.selected() == "conv-attn" {
		t.Errorf("pgdown didn't move")
	}
	h.key("end", "pgup")
	if h.selected() == "conv-gone" {
		t.Errorf("pgup didn't move")
	}
}

// The cursor stays on its conversation when a poll re-ranks the list.
func TestCursorFollowsItsConversation(t *testing.T) {
	api := &fakeAPI{page: samplePage(), workspaces: sampleWorkspaces()}
	h := newHarness(t, api, 80, 24)
	ticks := h.flush()
	h.key("j", "j", "j", "j") // conv-run
	if h.selected() != "conv-run" {
		t.Fatalf("on %q", h.selected())
	}
	api.mu.Lock()
	api.page.Conversations[0].Status = client.TaskNeedsAttention // conv-run now needs the user
	api.mu.Unlock()
	h.send(ticks[0].msg)
	h.flush()
	if h.selected() != "conv-run" {
		t.Errorf("after a re-rank: on %q, want conv-run", h.selected())
	}
}

// A long list scrolls to keep the cursor on screen, with its section's
// title above the first row of a section.
func TestScrolling(t *testing.T) {
	var rows []client.ConversationSummary
	for i := range 40 {
		rows = append(rows, client.ConversationSummary{ConversationID: "c" + string(rune('A'+i)), Status: client.TaskCompleted, Preview: "row " + string(rune('A'+i)), UpdatedAt: t0.Add(-time.Duration(i) * time.Minute)})
	}
	h := newHarness(t, &fakeAPI{page: &client.ConversationPage{Conversations: rows}}, 80, 15)
	h.flush()
	h.key("G")
	f := h.frame()
	if !strings.Contains(f, `"row `+string(rune('A'+39))) || strings.Contains(f, "DONE") {
		t.Errorf("at the bottom:\n%s", f)
	}
	if n := len(h.lines()); n != 15 {
		t.Errorf("frame is %d lines, want 15", n)
	}
	h.key("g")
	if f := h.frame(); !strings.Contains(f, "DONE (40)") {
		t.Errorf("back at the top, the section title is gone:\n%s", f)
	}
}

// tab shows needs you, running, done, then all again.
func TestTabCyclesTheGroups(t *testing.T) {
	h := loaded(t, 80, 24)
	h.key("tab")
	f := h.frame()
	if !strings.Contains(f, "NEEDS YOU (4)") || strings.Contains(f, "RUNNING") || strings.Contains(f, "summarise") {
		t.Errorf("needs you:\n%s", f)
	}
	h.key("tab")
	if f := h.frame(); !strings.Contains(f, "RUNNING (1)") || strings.Contains(f, "NEEDS YOU") || h.selected() != "conv-run" {
		t.Errorf("running (cursor %q):\n%s", h.selected(), f)
	}
	h.key("tab")
	if f := h.frame(); !strings.Contains(f, "DONE (4)") || strings.Contains(f, "RUNNING") {
		t.Errorf("done:\n%s", f)
	}
	h.key("tab")
	if f := h.frame(); !strings.Contains(f, "NEEDS YOU") || !strings.Contains(f, "RUNNING") || !strings.Contains(f, "DONE") {
		t.Errorf("all:\n%s", f)
	}
}

// A group with nothing in it says so.
func TestEmptyGroup(t *testing.T) {
	page := &client.ConversationPage{Conversations: []client.ConversationSummary{{ConversationID: "a", Status: client.TaskCompleted, UpdatedAt: t0}}}
	h := newHarness(t, &fakeAPI{page: page}, 80, 24)
	h.flush()
	h.key("tab")
	if f := h.frame(); !strings.Contains(f, "NEEDS YOU (0)") || !strings.Contains(f, "nothing needs you") {
		t.Errorf("empty needs you:\n%s", f)
	}
	h.key("tab")
	if f := h.frame(); !strings.Contains(f, "nothing is running") {
		t.Errorf("empty running:\n%s", f)
	}
	empty := newHarness(t, &fakeAPI{page: &client.ConversationPage{}}, 80, 24)
	empty.flush()
	if f := empty.frame(); !strings.Contains(f, "no conversations yet") {
		t.Errorf("no conversations:\n%s", f)
	}
}

// / opens the filter: typing narrows the list at once, enter keeps the
// filter, esc drops it. Keys that type text type it.
func TestFilter(t *testing.T) {
	h := loaded(t, 80, 24)
	h.key("/")
	h.typed("q")
	if h.app.quitting {
		t.Fatal("q in the filter quit")
	}
	h.key("backspace")
	h.typed("hub")
	f := h.frame()
	if strings.Contains(f, "summarise") || !strings.Contains(f, "kubectl") || !strings.Contains(f, "triage") {
		t.Errorf("filtered on hub:\n%s", f)
	}
	if !strings.Contains(f, "/hub") {
		t.Errorf("the filter's input isn't shown:\n%s", f)
	}
	if c := h.app.View().Cursor; c == nil {
		t.Error("no cursor in the filter's input")
	}
	h.key("enter")
	if h.app.screen != keymap.ListScreen {
		t.Fatalf("enter left the screen %s", h.app.screen)
	}
	h.key("j")
	if h.selected() != "conv-held" {
		t.Errorf("j in the filtered list: on %q, want conv-held", h.selected())
	}
	if f := h.frame(); !strings.Contains(f, `filter: hub`) || strings.Contains(f, "summarise") {
		t.Errorf("the kept filter:\n%s", f)
	}
	// esc on the list drops a kept filter.
	h.key("esc")
	if f := h.frame(); !strings.Contains(f, "summarise") || strings.Contains(f, "filter:") {
		t.Errorf("after esc:\n%s", f)
	}
	// esc in the filter's input drops it too.
	h.key("/")
	h.typed("nginx")
	if f := h.frame(); !strings.Contains(f, `no conversation matches "nginx"`) {
		t.Errorf("no match:\n%s", f)
	}
	h.key("esc")
	if f := h.frame(); !strings.Contains(f, "summarise") || h.app.screen != keymap.ListScreen {
		t.Errorf("after esc in the input:\n%s", f)
	}
	// The filter and the group combine.
	h.key("/")
	h.typed("notes")
	h.key("enter", "tab")
	if f := h.frame(); !strings.Contains(f, "draft the release") || strings.Contains(f, "summarise") {
		t.Errorf("needs you + notes:\n%s", f)
	}
}

// / again shows the kept filter in the input, to edit.
func TestFilterReopens(t *testing.T) {
	h := loaded(t, 80, 24)
	h.key("/")
	h.typed("hub")
	h.key("enter", "/")
	h.typed(" kub")
	h.key("enter")
	if f := h.frame(); !strings.Contains(f, "filter: hub kub") || strings.Contains(f, "triage") {
		t.Errorf("edited filter:\n%s", f)
	}
}

// ? shows the keys in effect, from the effective keymap; esc or ? closes
// it.
func TestHelp(t *testing.T) {
	keys, err := keymap.New(map[string][]string{"attach": {"A"}})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarnessKeys(t, &fakeAPI{page: samplePage()}, 80, 40, keys)
	h.flush()
	h.key("?")
	f := h.frame()
	for _, w := range []string{"attach to the agent", "A ", "keep the filter", "show needs you, running, done or all"} {
		if !strings.Contains(f, w) {
			t.Errorf("help lacks %q:\n%s", w, f)
		}
	}
	if strings.Contains(f, "kubectl") {
		t.Errorf("help shows the list:\n%s", f)
	}
	h.key("esc")
	if !strings.Contains(h.frame(), "kubectl") {
		t.Errorf("esc didn't close the help:\n%s", h.frame())
	}
	h.key("?", "?")
	if !strings.Contains(h.frame(), "kubectl") {
		t.Errorf("? didn't close the help:\n%s", h.frame())
	}
}

// A long help scrolls.
func TestHelpScrolls(t *testing.T) {
	h := loaded(t, 80, 15)
	h.key("?")
	first := h.frame()
	h.key("G")
	if h.frame() == first {
		t.Errorf("G didn't scroll the help:\n%s", first)
	}
	if n := len(h.lines()); n != 15 {
		t.Errorf("help is %d lines, want 15", n)
	}
}

// q quits, from the list and from the help.
func TestQuit(t *testing.T) {
	for _, keys := range [][]string{{"q"}, {"?", "q"}} {
		h := loaded(t, 80, 24)
		for _, k := range keys {
			h.send(keyPress(k))
		}
		if !slices.ContainsFunc(h.out, func(m tea.Msg) bool { _, ok := m.(tea.QuitMsg); return ok }) {
			t.Errorf("%v didn't quit: %v", keys, h.out)
		}
	}
}

// The actions later tickets add say they aren't here yet, rather than
// doing nothing.
func TestActionsNotYetHere(t *testing.T) {
	for k, want := range map[string]string{
		"enter": "opening a conversation",
		"n":     "starting a conversation",
		"a":     "attaching",
		"w":     "watching",
		"o":     "opening it in the browser",
	} {
		h := loaded(t, 80, 24)
		h.key(k)
		if f := h.frame(); !strings.Contains(f, want) || !strings.Contains(f, "isn't in this version yet") {
			t.Errorf("%s:\n%s", k, f)
		}
		h.key("j")
		if f := h.frame(); strings.Contains(f, "isn't in this version yet") {
			t.Errorf("the notice outlived the next key:\n%s", f)
		}
	}
}

// Under 60×15 the screen asks for more room rather than breaking.
func TestTooSmall(t *testing.T) {
	for _, size := range [][2]int{{59, 24}, {80, 14}} {
		h := loaded(t, size[0], size[1])
		f := h.frame()
		if strings.Contains(f, "\n") || !strings.Contains(f, "60×15") || ansi.StringWidth(f) > size[0] {
			t.Errorf("%dx%d: %q", size[0], size[1], f)
		}
	}
}

// Every line fits the terminal's width, at any width from the minimum
// up, and the frame fills its height.
func TestFrameFits(t *testing.T) {
	for _, width := range []int{60, 61, 72, 80, 100, 120, 200} {
		for _, height := range []int{15, 24, 40} {
			h := loaded(t, width, height)
			h.key("/")
			h.typed("e")
			for _, screen := range []string{"filter", "list", "help"} {
				switch screen {
				case "list":
					h.key("enter")
				case "help":
					h.key("?")
				}
				lines := h.lines()
				if len(lines) != height {
					t.Errorf("%dx%d %s: %d lines", width, height, screen, len(lines))
				}
				for i, l := range lines {
					if w := ansi.StringWidth(l); w > width {
						t.Errorf("%dx%d %s: line %d is %d wide: %q", width, height, screen, i, w, l)
					}
				}
			}
		}
	}
}

// has_more: the list says older conversations aren't shown.
func TestHasMore(t *testing.T) {
	page := samplePage()
	page.HasMore = true
	h := newHarness(t, &fakeAPI{page: page, workspaces: sampleWorkspaces()}, 80, 24)
	h.flush()
	if f := h.frame(); !strings.Contains(f, "only the newest 100") {
		t.Errorf("has_more:\n%s", f)
	}
}

// The selected row is marked without colour too.
func TestSelectedRowMarked(t *testing.T) {
	h := loaded(t, 80, 24)
	for _, l := range h.lines() {
		if strings.Contains(l, "kubectl") && !strings.HasPrefix(l, "›") {
			t.Errorf("selected row %q isn't marked", l)
		}
		if strings.Contains(l, "summarise") && strings.HasPrefix(l, "›") {
			t.Errorf("unselected row %q is marked", l)
		}
	}
}

// Bubble Tea v2 names keys as the keymap reads them: every default key,
// pressed, is its action on the list.
func TestBubbleTeaKeysMatchTheKeymap(t *testing.T) {
	m := keymap.Default()
	for _, c := range []struct {
		msg    tea.KeyPressMsg
		screen keymap.Screen
		want   keymap.Action
	}{
		{tea.KeyPressMsg{Code: 'j', Text: "j"}, keymap.ListScreen, keymap.Down},
		{tea.KeyPressMsg{Code: 'g', ShiftedCode: 'G', Text: "G", Mod: tea.ModShift}, keymap.ListScreen, keymap.Bottom},
		{tea.KeyPressMsg{Code: tea.KeyDown}, keymap.ListScreen, keymap.Down},
		{tea.KeyPressMsg{Code: tea.KeyPgDown}, keymap.ListScreen, keymap.PageDown},
		{tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, keymap.ListScreen, keymap.PageDown},
		{tea.KeyPressMsg{Code: tea.KeyHome}, keymap.ListScreen, keymap.Top},
		{tea.KeyPressMsg{Code: tea.KeyEnter}, keymap.ListScreen, keymap.Open},
		{tea.KeyPressMsg{Code: tea.KeyEscape}, keymap.ListScreen, keymap.Back},
		{tea.KeyPressMsg{Code: tea.KeyTab}, keymap.ListScreen, keymap.NextGroup},
		{tea.KeyPressMsg{Code: '/', Text: "/"}, keymap.ListScreen, keymap.Filter},
		{tea.KeyPressMsg{Code: '?', Text: "?"}, keymap.ListScreen, keymap.Help},
		{tea.KeyPressMsg{Code: tea.KeyEnter}, keymap.FilterScreen, keymap.Apply},
		{tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}, keymap.ComposerScreen, keymap.Newline},
		{tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, keymap.ComposerScreen, keymap.Cancel},
	} {
		got, _, ok := m.Lookup(c.screen, c.msg.String())
		if !ok || got != c.want {
			t.Errorf("%s on %s (%q) = %q, %v; want %q", c.msg.Keystroke(), c.screen, c.msg.String(), got, ok, c.want)
		}
	}
	// The space bar and Ctrl+Space, as v2 names them, are keys the keymap reads.
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeySpace, Text: " "}, {Code: tea.KeySpace, Mod: tea.ModCtrl}, {Code: tea.KeySpace, Mod: tea.ModAlt}} {
		if _, err := keymap.Canonical(k.String()); err != nil {
			t.Errorf("%q: %v", k.String(), err)
		}
	}
}

// Golden frames (the design's "Testing": the list at 80×24 and
// 120×40), with styles stripped. go test ./ui -update rewrites them.
func TestGoldenFrames(t *testing.T) {
	for _, c := range []struct {
		name          string
		width, height int
		keys          []string
		typed         string
	}{
		{"list-80x24", 80, 24, nil, ""},
		{"list-120x40", 120, 40, nil, ""},
		{"list-60x15", 60, 15, []string{"j", "j"}, ""},
		{"filter-80x24", 80, 24, []string{"/"}, "hub"},
		{"needs-you-80x24", 80, 24, []string{"tab"}, ""},
		{"help-80x24", 80, 24, []string{"?"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := loaded(t, c.width, c.height)
			h.key(c.keys...)
			h.typed(c.typed)
			golden(t, c.name, h.frame())
		})
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./ui -update to write it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs (go test ./ui -update rewrites it):\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// In one group's view, a filter that matches nothing there still names
// the group.
func TestFilterNoMatchInAGroup(t *testing.T) {
	h := loaded(t, 80, 24)
	h.key("tab", "tab") // running
	h.key("/")
	h.typed("kubectl")
	f := h.frame()
	if !strings.Contains(f, "RUNNING (0)") || !strings.Contains(f, `no conversation matches "kubectl"`) {
		t.Errorf("running, filtered on kubectl:\n%s", f)
	}
}

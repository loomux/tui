package conversation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
)

// fakeAPI answers the screen's fetches with what a test sets, and
// records them. The calls against a real server are in real_test.go.
type fakeAPI struct {
	conv        func(client.Page) (*client.Conversation, error)
	convCalls   []client.Page
	turns       func(taskID string, p client.Page) (*client.Transcript, error)
	turnsCalls  []turnsCall
	convIDs     []string
	cancelledBy []error // each fetch's context error when it was answered
	deadlines   []bool  // whether each fetch's context had a deadline
}

type turnsCall struct {
	task string
	page client.Page
}

func (f *fakeAPI) Conversation(ctx context.Context, id string, p client.Page) (*client.Conversation, error) {
	f.convIDs = append(f.convIDs, id)
	f.convCalls = append(f.convCalls, p)
	f.cancelledBy = append(f.cancelledBy, ctx.Err())
	_, deadline := ctx.Deadline()
	f.deadlines = append(f.deadlines, deadline)
	return f.conv(p)
}

func (f *fakeAPI) Transcript(ctx context.Context, taskID string, p client.Page) (*client.Transcript, error) {
	f.turnsCalls = append(f.turnsCalls, turnsCall{taskID, p})
	return f.turns(taskID, p)
}

func (f *fakeAPI) OpenStream(string) *client.Stream {
	panic("the tests' stream is the harness's")
}

// fakeStream is a stream a test scripts: Next gives the queued events,
// then fails with err, or with ErrStreamClosed when there is nothing
// more for now (the screen stops reading, as after its own Close).
type fakeStream struct {
	events []client.StreamEvent
	err    error
	nexts  int
	closed bool
}

func (s *fakeStream) Next(context.Context) (client.StreamEvent, error) {
	s.nexts++
	switch {
	case s.closed:
		return client.StreamEvent{}, client.ErrStreamClosed
	case len(s.events) > 0:
		ev := s.events[0]
		s.events = s.events[1:]
		return ev, nil
	case s.err != nil:
		return client.StreamEvent{}, s.err
	}
	return client.StreamEvent{}, client.ErrStreamClosed
}

func (s *fakeStream) Close() error {
	s.closed = true
	return nil
}

// timer is a wake-up the screen asked for.
type timer struct {
	d    time.Duration
	fire func(time.Time) tea.Msg
}

// harness runs a screen as a program would, one message at a time: each
// command a message returns is run, and its message fed back, until
// nothing is left.
type harness struct {
	t      *testing.T
	m      *Model
	api    *fakeAPI
	stream *fakeStream
	timers []timer
	// out is what the screen told the root: BackMsg, SignedOutMsg and
	// tea.QuitMsg.
	out []tea.Msg
	// held commands wait for release: a fetch in flight.
	holding bool
	held    []tea.Cmd
}

// fixture is a conversation of one finished exchange.
func fixture() *client.Conversation {
	return &client.Conversation{
		ConversationID: "conv-1",
		Messages: []client.Message{
			userMsg("m1", "d1", "fix the flaky ConversationPage test", time.Minute),
			replyMsg("m2", "d1", "I fixed **two** things:\n\n- the `retry` loop\n- the timeout", 3*time.Minute),
		},
		Tasks: []client.Task{{ID: "t1", Kind: "agent", AgentType: "claude-code", Status: client.TaskAwaitingInput,
			CreatedAt: at(time.Minute), UpdatedAt: at(3 * time.Minute)}},
		Dispatches: []client.Dispatch{{DispatchID: "d1", Status: client.DispatchSucceeded,
			Reply: "I fixed **two** things:\n\n- the `retry` loop\n- the timeout", CreatedAt: at(time.Minute)}},
	}
}

// newHarness returns a screen of conversation conv-1 at 80x24, not yet
// opened, whose fetches answer with conv.
func newHarness(t *testing.T, conv *client.Conversation, opts ...func(*Options)) *harness {
	t.Helper()
	h := &harness{t: t, api: &fakeAPI{}, stream: &fakeStream{}}
	h.api.conv = func(client.Page) (*client.Conversation, error) { return conv, nil }
	o := Options{Client: h.api, ID: "conv-1", Profile: "test", Title: "cc-hub", Width: 80, Height: 24,
		Now: func() time.Time { return at(10 * time.Minute) }}
	for _, opt := range opts {
		opt(&o)
	}
	h.m = New(o)
	h.m.open = func(string) eventStream { return h.stream }
	h.m.after = func(d time.Duration, fire func(time.Time) tea.Msg) tea.Cmd {
		h.timers = append(h.timers, timer{d, fire})
		return nil
	}
	return h
}

// opened returns a screen that has loaded conv and whose stream is
// connected.
func opened(t *testing.T, conv *client.Conversation, opts ...func(*Options)) *harness {
	t.Helper()
	h := newHarness(t, conv, opts...)
	h.stream.events = []client.StreamEvent{{Type: client.EventConnected}}
	h.open()
	return h
}

func (h *harness) open() { h.run(h.m.Init()) }

func (h *harness) send(msg tea.Msg) {
	m, cmd := h.m.Update(msg)
	if m != h.m {
		h.t.Fatalf("Update returned another model")
	}
	h.run(cmd)
}

func (h *harness) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if h.holding {
		h.held = append(h.held, cmd)
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	case BackMsg, SignedOutMsg, tea.QuitMsg:
		h.out = append(h.out, msg)
	default:
		h.send(msg)
	}
}

// hold keeps the commands asked for from now on in flight, and release
// lets them answer.
func (h *harness) hold() { h.holding = true }

func (h *harness) release() {
	h.holding = false
	held := h.held
	h.held = nil
	for _, c := range held {
		h.run(c)
	}
}

// event delivers one stream event.
func (h *harness) event(ev client.StreamEvent) {
	h.send(streamMsg{inst: h.m.inst, ev: ev})
}

// fire lets the i-th timer the screen asked for go off.
func (h *harness) fire(i int) {
	h.t.Helper()
	if i >= len(h.timers) {
		h.t.Fatalf("no timer %d: the screen asked for %d", i, len(h.timers))
	}
	h.send(h.timers[i].fire(time.Time{}))
}

var namedKeys = map[string]rune{
	"esc": tea.KeyEscape, "enter": tea.KeyEnter, "tab": tea.KeyTab,
	"up": tea.KeyUp, "down": tea.KeyDown, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
	"home": tea.KeyHome, "end": tea.KeyEnd,
}

// press is the key press Bubble Tea reports for a key written as the
// keymap writes it: "p", "G", "esc", "ctrl+b".
func press(key string) tea.KeyPressMsg {
	var k tea.KeyPressMsg
	if rest, ok := strings.CutPrefix(key, "ctrl+"); ok {
		k.Mod, key = tea.ModCtrl, rest
	}
	if code, ok := namedKeys[key]; ok {
		k.Code = code
		return k
	}
	r := []rune(key)
	if len(r) != 1 {
		panic("press: not a key: " + key)
	}
	k.Code = r[0]
	if k.Mod == 0 {
		k.Text = key
	}
	return k
}

func (h *harness) key(keys ...string) {
	for _, k := range keys {
		h.send(press(k))
	}
}

// screen is what the screen shows, without its styling.
func (h *harness) screen() string { return ansi.Strip(h.m.View()) }

func (h *harness) lines() []string { return strings.Split(h.screen(), "\n") }

func (h *harness) has(texts ...string) {
	h.t.Helper()
	s := h.screen()
	for _, text := range texts {
		if !strings.Contains(s, text) {
			h.t.Errorf("the screen doesn't show %q:\n%s", text, s)
		}
	}
}

func (h *harness) lacks(texts ...string) {
	h.t.Helper()
	s := h.screen()
	for _, text := range texts {
		if strings.Contains(s, text) {
			h.t.Errorf("the screen shows %q:\n%s", text, s)
		}
	}
}

// rowOf is the row of the screen's first line showing text, or -1.
func (h *harness) rowOf(text string) int {
	return slices.IndexFunc(h.lines(), func(l string) bool { return strings.Contains(l, text) })
}

// fetches is how many times the conversation was fetched.
func (h *harness) fetches() int { return len(h.api.convCalls) }

func messageAdded(id string) client.StreamEvent {
	return client.StreamEvent{Type: client.EventMessageAdded, Message: &client.MessageAdded{MessageID: id, Role: client.RoleAssistant}}
}

func dispatchUpdate(id, status, reply, errText string) client.StreamEvent {
	return client.StreamEvent{Type: client.EventDispatchUpdate,
		Dispatch: &client.DispatchUpdate{DispatchID: id, Status: status, Reply: reply, Error: errText}}
}

func taskUpdate(id, status string, d time.Duration) client.StreamEvent {
	return client.StreamEvent{Type: client.EventTaskUpdate, Task: &client.TaskUpdate{TaskID: id, Status: status, UpdatedAt: at(d)}}
}

func TestOpeningFetchesTheNewestPageAndShowsIt(t *testing.T) {
	h := newHarness(t, fixture())
	h.open()

	if want := []client.Page{{Limit: 50}}; !slices.Equal(h.api.convCalls, want) {
		t.Errorf("fetches = %+v, want %+v", h.api.convCalls, want)
	}
	if want := []string{"conv-1"}; !slices.Equal(h.api.convIDs, want) {
		t.Errorf("fetched %v, want %v", h.api.convIDs, want)
	}
	if !h.api.deadlines[0] {
		t.Error("the fetch has no deadline: one that never answered would hold back every later one")
	}
	h.has("loomux · test · cc-hub", "connecting…",
		"you · 12:01", "fix the flaky ConversationPage test",
		"loomux · 12:03", "I fixed two things:", "• the", "retry", "• the timeout")
	h.lacks("**two**", "loading", "● live")

	h.event(client.StreamEvent{Type: client.EventConnected})
	h.has("● live")
	h.lacks("connecting…")
}

func TestHeaderNamesTheConversationByItsIDWithoutATitle(t *testing.T) {
	h := opened(t, fixture(), func(o *Options) { o.Title, o.ID = "", "0f4c2a9e-77aa-4b1c-9d2e-5a1b2c3d4e5f" })
	h.has("loomux · test · 0f4c2a9e")
	h.lacks("77aa")
}

func TestUntilTheFirstAnswerItSaysLoading(t *testing.T) {
	h := newHarness(t, fixture())
	h.hold()
	h.open()
	h.has("loading…")
	h.lacks("fix the flaky", "no messages")

	h.release()
	h.has("fix the flaky ConversationPage test")
	h.lacks("loading…")
}

func TestAConversationWithoutMessagesSaysSo(t *testing.T) {
	h := opened(t, &client.Conversation{ConversationID: "conv-1"})
	h.has("no messages yet")
}

// A conversation is opened by its id before its first message is sent:
// the server has nothing under it yet, which isn't an error.
func TestAConversationTheServerDoesNotKnowYetIsEmpty(t *testing.T) {
	h := newHarness(t, nil)
	h.api.conv = func(client.Page) (*client.Conversation, error) {
		return nil, &client.Error{Status: http.StatusNotFound, Code: client.CodeNotFound, Message: "conversation not found"}
	}
	h.open()
	h.has("no messages yet")
	h.lacks("not found", "⚠")
}

func TestAFailedLoadShowsTheServersErrorAndTheNextConnectTriesAgain(t *testing.T) {
	h := newHarness(t, fixture())
	fail := true
	h.api.conv = func(client.Page) (*client.Conversation, error) {
		if fail {
			return nil, &client.Error{Status: http.StatusInternalServerError, Code: client.CodeInternal, Message: "the store is locked"}
		}
		return fixture(), nil
	}
	h.open()
	h.has("the store is locked")
	h.lacks("no messages yet", "loading…")

	fail = false
	h.event(client.StreamEvent{Type: client.EventConnected})
	h.has("fix the flaky ConversationPage test")
	h.lacks("the store is locked")
}

// The messages on screen stay when a later fetch fails: the error goes
// in the header.
func TestAFailedRefetchKeepsWhatIsShown(t *testing.T) {
	h := opened(t, fixture())
	h.api.conv = func(client.Page) (*client.Conversation, error) {
		return nil, errors.New("connection refused")
	}
	h.event(messageAdded("m3"))

	h.has("fix the flaky ConversationPage test", "⚠ connection refused")
}

func TestA401SignsOutOnce(t *testing.T) {
	unauthorized := &client.Error{Status: http.StatusUnauthorized, Code: client.CodeUnauthorized, Message: "session expired"}
	t.Run("from a fetch", func(t *testing.T) {
		h := newHarness(t, nil)
		h.api.conv = func(client.Page) (*client.Conversation, error) { return nil, unauthorized }
		h.stream.events = []client.StreamEvent{{Type: client.EventConnected}, messageAdded("m9")}
		h.open()

		if len(h.out) != 1 {
			t.Fatalf("told the root %v, want one SignedOutMsg", h.out)
		}
		if got, ok := h.out[0].(SignedOutMsg); !ok || !errors.Is(got.Err, unauthorized) {
			t.Errorf("told the root %#v, want SignedOutMsg with the 401", h.out[0])
		}
		if !h.stream.closed {
			t.Error("the stream is still open")
		}
		if h.fetches() != 1 {
			t.Errorf("%d fetches, want no more after the 401", h.fetches())
		}
		h.has("signed out", "loomux login")
	})
	t.Run("from the stream", func(t *testing.T) {
		h := newHarness(t, fixture())
		h.stream.err = unauthorized
		h.open()

		if len(h.out) != 1 {
			t.Fatalf("told the root %v, want one SignedOutMsg", h.out)
		}
		if _, ok := h.out[0].(SignedOutMsg); !ok {
			t.Errorf("told the root %#v, want SignedOutMsg", h.out[0])
		}
		h.has("signed out")
	})
}

func TestMessageAddedFetchesTheNewestPageAgain(t *testing.T) {
	conv := fixture()
	h := opened(t, conv)
	conv.Messages = append(conv.Messages, replyMsg("m3", "", "The nightly run passed too.", 8*time.Minute))
	h.lacks("The nightly run passed too.")
	before := h.fetches()

	h.event(messageAdded("m3"))

	if got, want := h.api.convCalls[before:], []client.Page{{Limit: 50}}; !slices.Equal(got, want) {
		t.Errorf("fetches after the event = %+v, want %+v", got, want)
	}
	h.has("The nightly run passed too.", "loomux · 12:08")
}

// secondTurn adds to conv what a message sent from another client leaves
// on the server: the message and its dispatch, in status.
func secondTurn(conv *client.Conversation, status string) *client.Dispatch {
	conv.Messages = append(conv.Messages, userMsg("m3", "d2", "and the nightly?", 7*time.Minute))
	conv.Dispatches = append(conv.Dispatches, client.Dispatch{DispatchID: "d2", Status: status, CreatedAt: at(7 * time.Minute)})
	return &conv.Dispatches[len(conv.Dispatches)-1]
}

func TestATurnInFlightShowsUntilItsReplyDoes(t *testing.T) {
	conv := fixture()
	h := opened(t, conv)
	d2 := secondTurn(conv, client.DispatchQueued)
	h.event(dispatchUpdate("d2", client.DispatchQueued, "", ""))
	h.has("and the nightly?", "◐ queued… since 12:07")

	// From here on the events alone say it: the server's answers stay
	// behind, as a fetch asked a moment too early would.
	before := h.fetches()
	h.event(dispatchUpdate("d2", client.DispatchRunning, "", ""))
	h.has("◐ working… since 12:07")
	h.lacks("queued")

	h.event(dispatchUpdate("d2", client.DispatchSucceeded, "All **green** now.", ""))
	h.has("All green now.")
	h.lacks("◐ working", "**green**")
	if h.fetches() != before {
		t.Errorf("the turn's progress waited for a fetch: %d more", h.fetches()-before)
	}
	if d2.Status != client.DispatchQueued {
		t.Fatal("the fixture changed under the test")
	}
}

// A turn first heard of on the stream is fetched: its message isn't
// loaded.
func TestATurnTheScreenDidNotKnowIsFetched(t *testing.T) {
	conv := fixture()
	h := opened(t, conv)
	before := h.fetches()
	secondTurn(conv, client.DispatchRunning)
	h.event(dispatchUpdate("d2", client.DispatchRunning, "", ""))

	if h.fetches() != before+1 {
		t.Errorf("%d fetches for a new turn, want 1", h.fetches()-before)
	}
	h.has("and the nightly?", "◐ working")
}

func TestAReplyIsShownOnceWhenItsMessageArrives(t *testing.T) {
	conv := fixture()
	h := opened(t, conv)
	d2 := secondTurn(conv, client.DispatchRunning)
	h.event(dispatchUpdate("d2", client.DispatchRunning, "", ""))
	h.event(dispatchUpdate("d2", client.DispatchSucceeded, "All green now.", ""))
	if n := strings.Count(h.screen(), "All green now."); n != 1 {
		t.Fatalf("before its message is fetched the reply shows %d times:\n%s", n, h.screen())
	}

	d2.Status, d2.Reply = client.DispatchSucceeded, "All green now."
	conv.Messages = append(conv.Messages, replyMsg("m4", "d2", "All green now.", 8*time.Minute))
	h.event(messageAdded("m4"))

	if n := strings.Count(h.screen(), "All green now."); n != 1 {
		t.Errorf("the reply shows %d times:\n%s", n, h.screen())
	}
	h.has("and the nightly?")
}

func TestATurnThatFailedShowsWhy(t *testing.T) {
	conv := fixture()
	h := opened(t, conv)
	secondTurn(conv, client.DispatchRunning)
	h.event(dispatchUpdate("d2", client.DispatchRunning, "", ""))
	h.event(dispatchUpdate("d2", client.DispatchFailed, "", "no target is registered for workspace demo"))

	h.has("✗ the turn failed: no target is registered for workspace demo")
	h.lacks("◐ working")
}

func TestAnInterruptedTurnSaysInterrupted(t *testing.T) {
	conv := fixture()
	h := opened(t, conv)
	d2 := secondTurn(conv, client.DispatchInterrupted)
	d2.Error = "the server restarted"
	h.event(dispatchUpdate("d2", client.DispatchInterrupted, "", "the server restarted"))

	h.has("✗ the turn was interrupted: the server restarted")
}

func TestTaskUpdateChangesTheTasksStatusLine(t *testing.T) {
	conv := fixture()
	h := opened(t, conv)
	h.has("● awaiting input", "claude-code")

	conv.Tasks[0].Status, conv.Tasks[0].UpdatedAt = client.TaskRunning, at(5*time.Minute)
	h.hold() // the status shows from the event, before any fetch answers
	h.event(taskUpdate("t1", client.TaskRunning, 5*time.Minute))
	h.has("◐ running", "claude-code")
	h.lacks("awaiting input")
	h.release()
	h.has("◐ running")
}

func TestStatusLinesSayWhyATaskNeedsTheUser(t *testing.T) {
	conv := fixture()
	conv.Tasks = []client.Task{
		{ID: "t1", Kind: "agent", AgentType: "claude-code", Status: client.TaskNeedsAttention, UpdatedAt: at(4 * time.Minute),
			Attention: &client.Attention{Kind: "permission", Title: "Bash command", Question: "Do you want to proceed?"}},
		{ID: "t2", Kind: "agent", AgentType: "codex", Status: client.TaskFailed, UpdatedAt: at(3 * time.Minute),
			ErrorClass: "agent_rate_limited", FailureReason: "usage limit reached, resets 15:00"},
		{ID: "t3", Kind: "agent", AgentType: "claude-code", Status: client.TaskHumanTakeover, UpdatedAt: at(2 * time.Minute)},
	}
	h := opened(t, conv)

	h.has("▲ needs attention", "Bash command", "⌨ taken over")
	// The failed task is over: only tasks still open take a line while
	// there are any.
	h.lacks("usage limit reached")
}

// The status lines never take more than three rows of the window.
func TestMoreOpenTasksThanLinesAreCounted(t *testing.T) {
	conv := fixture()
	conv.Tasks = nil
	for i, agent := range []string{"agent-a", "agent-b", "agent-c", "agent-d", "agent-e"} {
		conv.Tasks = append(conv.Tasks, client.Task{ID: agent, Kind: "agent", AgentType: agent, Status: client.TaskRunning,
			UpdatedAt: at(time.Duration(i) * time.Minute)})
	}
	h := opened(t, conv)

	// The two most recently updated, then the rest as a number.
	h.has("agent-e", "agent-d", "… and 3 more open tasks")
	h.lacks("agent-c", "agent-b", "agent-a")

	conv.Tasks = conv.Tasks[2:]
	h.event(messageAdded("m9"))
	h.has("agent-e", "agent-d", "agent-c")
	h.lacks("more open task")
}

// An event the client couldn't read still says something changed.
func TestAnEventThatDidNotDecodeFetchesTheConversation(t *testing.T) {
	h := opened(t, fixture())
	before := h.fetches()
	h.event(client.StreamEvent{Type: client.EventDispatchUpdate, Err: errors.New("a dispatch_update event: unexpected end of JSON input")})

	if got := h.fetches() - before; got != 1 {
		t.Errorf("%d fetches after an event that didn't decode, want 1", got)
	}
}

func TestHeaderCutsALongIDAtACharacter(t *testing.T) {
	h := opened(t, fixture(), func(o *Options) { o.Title, o.ID = "", "разговор-номер-один" })
	h.has("loomux · test · разговор   ")
}

func TestTheLatestTaskShowsWhenNoneIsOpen(t *testing.T) {
	conv := fixture()
	conv.Tasks = []client.Task{
		{ID: "t1", Kind: "agent", AgentType: "claude-code", Status: client.TaskCompleted, UpdatedAt: at(2 * time.Minute)},
		{ID: "t2", Kind: "agent", AgentType: "codex", Status: client.TaskFailed, UpdatedAt: at(3 * time.Minute),
			ErrorClass: "agent_rate_limited", FailureReason: "usage limit reached, resets 15:00"},
	}
	h := opened(t, conv)

	h.has("✗ failed", "codex", "agent_rate_limited: usage limit reached, resets 15:00")
	h.lacks("✓ completed")
}

func TestATaskUpdateThatChangesTheStatusFetchesTheRest(t *testing.T) {
	conv := fixture()
	h := opened(t, conv)
	conv.Tasks[0].Status, conv.Tasks[0].UpdatedAt = client.TaskNeedsAttention, at(5*time.Minute)
	conv.Tasks[0].Attention = &client.Attention{Title: "Bash command"}
	before := h.fetches()

	h.event(taskUpdate("t1", client.TaskNeedsAttention, 5*time.Minute))

	if h.fetches() != before+1 {
		t.Errorf("%d fetches, want one for the prompt", h.fetches()-before)
	}
	h.has("▲ needs attention", "Bash command")

	// The stream repeating the same state asks for nothing.
	h.event(taskUpdate("t1", client.TaskNeedsAttention, 5*time.Minute))
	if h.fetches() != before+1 {
		t.Errorf("%d fetches after an event that changed nothing, want 1", h.fetches()-before)
	}
}

// The stream replays nothing added before it opened. So each connect
// fetches: the first for what came between the opening fetch and the
// stream's start, a later one for what came during the drop.
func TestEveryConnectFetchesTheConversationAgain(t *testing.T) {
	conv := fixture()
	h := newHarness(t, conv)
	h.open()
	if h.fetches() != 1 {
		t.Fatalf("%d fetches on opening, want 1", h.fetches())
	}
	conv.Messages = append(conv.Messages, replyMsg("m3", "", "added before the stream opened", 4*time.Minute))
	h.event(client.StreamEvent{Type: client.EventConnected})
	if h.fetches() != 2 {
		t.Errorf("%d fetches after the first connect, want 2", h.fetches())
	}
	h.has("added before the stream opened")

	h.event(client.StreamEvent{Type: client.EventDisconnected, Err: errors.New("eof"), RetryIn: time.Second})
	conv.Messages = append(conv.Messages, replyMsg("m4", "", "added during the drop", 5*time.Minute))
	h.event(client.StreamEvent{Type: client.EventConnected, Reconnected: true})
	if h.fetches() != 3 {
		t.Errorf("%d fetches after a reconnect, want 3", h.fetches())
	}
	h.has("added during the drop")
}

func TestReconnectingShowsOnceADropHasLastedFiveSeconds(t *testing.T) {
	h := opened(t, fixture())
	h.event(client.StreamEvent{Type: client.EventDisconnected, Err: errors.New("eof"), RetryIn: time.Second})
	h.lacks("reconnecting")
	if len(h.timers) != 1 || h.timers[0].d != 5*time.Second {
		t.Fatalf("timers = %+v, want one of 5s", h.timers)
	}
	// The retries that fail in the meantime are the same drop.
	h.event(client.StreamEvent{Type: client.EventDisconnected, Err: errors.New("refused"), RetryIn: 2 * time.Second})
	if len(h.timers) != 1 {
		t.Fatalf("%d timers, want the drop's one", len(h.timers))
	}

	h.fire(0)
	h.has("reconnecting…")
	h.lacks("● live")

	h.event(client.StreamEvent{Type: client.EventConnected, Reconnected: true})
	h.has("● live")
	h.lacks("reconnecting")
}

func TestADropShorterThanFiveSecondsNeverShows(t *testing.T) {
	h := opened(t, fixture())
	h.event(client.StreamEvent{Type: client.EventDisconnected, Err: errors.New("eof"), RetryIn: time.Second})
	h.event(client.StreamEvent{Type: client.EventConnected, Reconnected: true})
	h.fire(0)
	h.lacks("reconnecting")
	h.has("● live")

	// A second drop has its own five seconds: the first one's timer
	// going off late says nothing about it.
	h.event(client.StreamEvent{Type: client.EventDisconnected, Err: errors.New("eof"), RetryIn: time.Second})
	h.fire(0)
	h.lacks("reconnecting")
	h.fire(1)
	h.has("reconnecting…")
}

func TestAStreamThatNeverConnectedSaysConnectingThenReconnecting(t *testing.T) {
	h := newHarness(t, fixture())
	h.open()
	h.has("connecting…")
	h.lacks("● live")

	h.event(client.StreamEvent{Type: client.EventDisconnected, Err: errors.New("refused"), RetryIn: time.Second})
	h.fire(0)
	h.has("reconnecting…")
}

// A fetch answers with the conversation as it was when it was asked. What
// the stream says while one is in flight asks for one more after it, not
// for one each.
func TestEventsDuringAFetchAskForOneMoreAfterIt(t *testing.T) {
	conv := fixture()
	h := opened(t, conv)
	before := h.fetches()
	h.hold()
	h.event(messageAdded("m3"))
	h.event(messageAdded("m4"))
	h.event(messageAdded("m5"))
	conv.Messages = append(conv.Messages, replyMsg("m5", "", "the last word", 9*time.Minute))
	h.release()

	if got := h.fetches() - before; got != 2 {
		t.Errorf("%d fetches, want 2: the one in flight, and one after it", got)
	}
	h.has("the last word")
}

// Even an event that needs no fetch of its own says the answer on its way
// may be out of date.
func TestAnEventThatNeedsNoFetchStillAsksForOneAfterTheOneInFlight(t *testing.T) {
	h := opened(t, fixture())
	before := h.fetches()
	h.hold()
	h.event(messageAdded("m3"))
	h.event(taskUpdate("t1", client.TaskAwaitingInput, 4*time.Minute)) // touched: the same status
	h.release()

	if got := h.fetches() - before; got != 2 {
		t.Errorf("%d fetches, want 2: the one in flight, and one after it", got)
	}
}

func TestEveryEventAsksForTheNextOne(t *testing.T) {
	h := opened(t, fixture())
	for i, ev := range []client.StreamEvent{
		messageAdded("m3"),
		dispatchUpdate("d2", client.DispatchRunning, "", ""),
		taskUpdate("t1", client.TaskRunning, 5*time.Minute),
		{Type: client.EventDisconnected, Err: errors.New("eof")},
		{Type: client.EventConnected, Reconnected: true},
		{Type: "task_hold", Data: []byte(`{"task_id":"t1"}`)},       // a later server's event
		{Type: client.EventTaskUpdate, Err: errors.New("bad json")}, // an event that didn't decode
	} {
		before := h.stream.nexts
		h.event(ev)
		if got := h.stream.nexts - before; got != 1 {
			t.Errorf("event %d (%s): Next called %d times after it, want 1", i, ev.Type, got)
		}
	}
	h.has("fix the flaky ConversationPage test")
}

// Next's contract is the 401, a closed stream or an ended context. Any
// other error means the stream is gone for good: say so.
func TestAStreamThatEndsForAnotherReasonSaysSo(t *testing.T) {
	h := newHarness(t, fixture())
	h.stream.err = errors.New("the stream broke")
	h.open()

	h.has("⚠ the stream broke", "fix the flaky ConversationPage test")
	if len(h.out) != 0 {
		t.Errorf("told the root %v", h.out)
	}
}

func TestCloseEndsTheStreamAndTheFetchesInFlight(t *testing.T) {
	h := opened(t, fixture())
	h.hold()
	h.event(messageAdded("m3"))
	h.m.Close()
	h.m.Close() // closing twice is harmless
	h.release()

	if !h.stream.closed {
		t.Error("the stream is still open")
	}
	if err := h.api.cancelledBy[len(h.api.cancelledBy)-1]; !errors.Is(err, context.Canceled) {
		t.Errorf("the fetch in flight ran on a context with err %v, want it cancelled", err)
	}
	if len(h.out) != 0 {
		t.Errorf("a closed screen told the root %v", h.out)
	}
}

// The root may still hold messages for a screen it has closed (a key
// typed ahead, an answer on its way): they do nothing.
func TestAClosedScreenTakesNoMoreMessages(t *testing.T) {
	conv := fixture()
	h := opened(t, conv)
	h.m.Close()
	fetches, nexts := h.fetches(), h.stream.nexts

	late := fixture()
	late.Messages = append(late.Messages, replyMsg("m3", "", "said after the screen closed", 9*time.Minute))
	h.send(loadedMsg{inst: h.m.inst, conv: late})
	h.event(messageAdded("m4"))
	h.key("q", "esc", "?", "p")

	h.lacks("said after the screen closed", "move up")
	if len(h.out) != 0 || h.fetches() != fetches || h.stream.nexts != nexts {
		t.Errorf("a closed screen told the root %v, fetched %d times and read its stream %d times",
			h.out, h.fetches()-fetches, h.stream.nexts-nexts)
	}
}

// Signed out, the screen still shows what it had and can be read and
// left, but asks the server for nothing: no answer would be taken.
func TestSignedOutAsksForNothingMore(t *testing.T) {
	h := newHarness(t, nil)
	h.api.conv = func(client.Page) (*client.Conversation, error) {
		return &client.Conversation{Messages: long(21, 40), HasMore: true, NextBefore: "m21",
			Tasks: []client.Task{{ID: "t1", AgentType: "claude-code", Status: client.TaskRunning}}}, nil
	}
	h.stream.err = &client.Error{Status: http.StatusUnauthorized, Code: client.CodeUnauthorized, Message: "session expired"}
	h.open()
	fetches := h.fetches()

	h.key("g", "k", "p")

	h.has("message 21", "loomux login")
	h.lacks("loading")
	if h.fetches() != fetches || len(h.api.turnsCalls) != 0 {
		t.Errorf("signed out, it fetched the conversation %d times and a transcript %d times", h.fetches()-fetches, len(h.api.turnsCalls))
	}
	h.key("esc")
	if len(h.out) != 2 || h.out[1] != (BackMsg{}) {
		t.Errorf("told the root %v, want SignedOutMsg then BackMsg", h.out)
	}
}

// The root may open another conversation while answers for this one are
// still on their way: they are another screen's.
func TestAnswersForAnotherScreenAreIgnored(t *testing.T) {
	h := opened(t, fixture())
	other := &client.Conversation{Messages: []client.Message{userMsg("x1", "", "another conversation entirely", 0)}}
	nexts, fetches := h.stream.nexts, h.fetches()

	h.send(loadedMsg{inst: h.m.inst + 1, conv: other})
	h.send(streamMsg{inst: h.m.inst + 1, ev: messageAdded("x2")})
	h.send(dropMsg{inst: h.m.inst + 1, drop: 0})

	h.lacks("another conversation entirely", "reconnecting")
	if h.fetches() != fetches || h.stream.nexts != nexts {
		t.Errorf("another screen's messages made this one fetch (%d more) or read its stream (%d more)", h.fetches()-fetches, h.stream.nexts-nexts)
	}
}

func TestBackLeavesAndEndsTheStream(t *testing.T) {
	h := opened(t, fixture())
	h.key("esc")

	if len(h.out) != 1 || h.out[0] != (BackMsg{}) {
		t.Errorf("told the root %v, want BackMsg", h.out)
	}
	if !h.stream.closed {
		t.Error("the stream is still open")
	}
}

func TestQuitQuitsAndEndsTheStream(t *testing.T) {
	h := opened(t, fixture())
	h.key("q")

	if len(h.out) != 1 {
		t.Fatalf("told the root %v, want tea.QuitMsg", h.out)
	}
	if _, ok := h.out[0].(tea.QuitMsg); !ok {
		t.Errorf("told the root %#v, want tea.QuitMsg", h.out[0])
	}
	if !h.stream.closed {
		t.Error("the stream is still open")
	}
}

func TestKeysAreTheEffectiveKeymaps(t *testing.T) {
	keys, err := keymap.New(map[string][]string{"back": {"ctrl+b"}, "transcript": {"P"}})
	if err != nil {
		t.Fatal(err)
	}
	h := opened(t, fixture(), func(o *Options) { o.Keys = keys })
	h.has("ctrl+b back", "P transcript")
	h.lacks("esc back")

	h.key("esc")
	if len(h.out) != 0 {
		t.Errorf("esc, no longer bound, told the root %v", h.out)
	}
	h.key("ctrl+b")
	if len(h.out) != 1 || h.out[0] != (BackMsg{}) {
		t.Errorf("ctrl+b told the root %v, want BackMsg", h.out)
	}
}

func TestTheFooterNamesTheKeys(t *testing.T) {
	h := opened(t, fixture())
	h.has("esc back", "p transcript", "k/j scroll", "q quit", "?=help")
}

// The keymap already has the keys of the tickets after this one. Until
// they are built, pressing one says so rather than doing nothing.
func TestAnActionOfALaterTicketSaysItIsNotHereYet(t *testing.T) {
	for key, what := range map[string]string{
		"a": "attaching", "w": "watching", "o": "opening it in the browser", "x": "cancelling a turn", "ctrl+c": "cancelling a turn",
		"y": "answering a prompt", "n": "answering a prompt", "3": "answering a prompt", "r": "replying",
	} {
		h := opened(t, fixture())
		h.key(key)
		h.has(what + " isn't in this version yet")

		h.key("j")
		h.lacks("isn't in this version yet")
		if len(h.out) != 0 {
			t.Errorf("%s told the root %v", key, h.out)
		}
	}
}

// long is a conversation of n one-line messages, "message 01" on, with
// the ids m01 on.
func long(from, to int) []client.Message {
	var ms []client.Message
	for i := from; i <= to; i++ {
		ms = append(ms, userMsg(fmt.Sprintf("m%02d", i), "", fmt.Sprintf("message %02d", i), time.Duration(i)*time.Minute))
	}
	return ms
}

func TestItOpensAtTheLatestMessageAndScrolls(t *testing.T) {
	h := opened(t, &client.Conversation{Messages: long(1, 40)})
	h.has("message 40")
	h.lacks("message 01")

	h.key("g")
	h.has("message 01")
	h.lacks("message 40")

	h.key("G")
	h.has("message 40")
	h.lacks("message 01")

	h.key("k")
	h.key("pgup")
	h.lacks("message 40")
	h.key("pgdown", "j")
	h.has("message 40")
}

func TestNewMessagesAreFollowedOnlyFromTheEnd(t *testing.T) {
	conv := &client.Conversation{Messages: long(1, 40)}
	h := opened(t, conv)

	conv.Messages = long(1, 41)
	h.event(messageAdded("m41"))
	h.has("message 41")

	// Scrolled up to read, the view stays where it is, and says there
	// is more below.
	h.key("g")
	conv.Messages = long(1, 42)
	h.event(messageAdded("m42"))
	h.has("message 01", "↓")
	h.lacks("message 42")

	h.key("G")
	h.has("message 42")
	h.lacks("↓")
}

func TestScrollingToTheTopLoadsTheOlderPage(t *testing.T) {
	h := newHarness(t, nil)
	h.api.conv = func(p client.Page) (*client.Conversation, error) {
		if p.Before == "m21" {
			return &client.Conversation{Messages: long(1, 20)}, nil
		}
		return &client.Conversation{Messages: long(21, 40), HasMore: true, NextBefore: "m21"}, nil
	}
	h.open()
	h.hold()
	h.key("g")
	h.has("loading older messages…")
	row := h.rowOf("you · 12:21")
	h.release()

	if want := []client.Page{{Limit: 50}, {Before: "m21", Limit: 50}}; !slices.Equal(h.api.convCalls, want) {
		t.Fatalf("fetches = %+v, want %+v", h.api.convCalls, want)
	}
	// The older messages go above: what was being read doesn't move.
	if got := h.rowOf("you · 12:21"); got != row || row < 0 {
		t.Errorf("message 21 moved from row %d to row %d when the older page arrived:\n%s", row, got, h.screen())
	}
	h.lacks("message 01", "loading older messages…")
	h.key("pgup")
	h.has("message 19")
	h.key("g")
	h.has("message 01")

	// Nothing is older than the first page.
	h.key("g", "k")
	if h.fetches() != 2 {
		t.Errorf("%d fetches, want 2: there is no older page", h.fetches())
	}
}

// A conversation whose newest page fits the window is at its top and at
// its end at once: only a key that goes up asks for what is older.
func TestOnlyGoingUpAsksForTheOlderPage(t *testing.T) {
	h := newHarness(t, nil)
	h.api.conv = func(p client.Page) (*client.Conversation, error) {
		if p.Before == "m39" {
			return &client.Conversation{Messages: long(37, 38)}, nil
		}
		return &client.Conversation{Messages: long(39, 40), HasMore: true, NextBefore: "m39"}, nil
	}
	h.open()
	h.has("↑ scroll up for older messages", "message 39", "message 40")

	h.key("j", "pgdown", "G")
	if h.fetches() != 1 {
		t.Fatalf("%d fetches after going down, want 1", h.fetches())
	}
	h.key("k")
	if h.fetches() != 2 {
		t.Errorf("%d fetches after going up at the top, want 2", h.fetches())
	}
	h.has("message 37", "message 40")
	h.lacks("scroll up for older messages")
}

func TestTheOlderPageIsAskedForOnce(t *testing.T) {
	h := newHarness(t, nil)
	h.api.conv = func(p client.Page) (*client.Conversation, error) {
		if p.Before == "m21" {
			return &client.Conversation{Messages: long(1, 20)}, nil
		}
		return &client.Conversation{Messages: long(21, 40), HasMore: true, NextBefore: "m21"}, nil
	}
	h.open()
	h.hold()
	h.key("g", "k", "g", "pgup")
	h.has("loading older messages…")
	h.release()

	if h.fetches() != 2 {
		t.Errorf("%d fetches, want 2: one page asked for once", h.fetches())
	}
	h.lacks("loading older messages…")
}

func TestHelpShowsTheScreensKeysAndClosesOnTheSameKey(t *testing.T) {
	h := opened(t, fixture())
	h.key("?")
	h.has("show the task's transcript", "esc", "back", "k/up", "move up")
	h.lacks("fix the flaky ConversationPage test")

	h.key("?")
	h.has("fix the flaky ConversationPage test")
	h.lacks("move up")

	h.key("?", "esc")
	h.has("fix the flaky ConversationPage test")
	if len(h.out) != 0 {
		t.Errorf("esc on the help told the root %v: it closes the help, not the conversation", h.out)
	}
}

func TestTheViewFillsTheWindowExactly(t *testing.T) {
	conv := fixture()
	conv.Messages = append(conv.Messages, long(3, 30)...)
	h := opened(t, conv)
	for _, size := range [][2]int{{80, 24}, {120, 40}, {60, 15}, {200, 60}, {20, 6}, {40, 5}, {40, 4}, {40, 3}, {5, 2}, {1, 1}} {
		w, hgt := size[0], size[1]
		h.send(tea.WindowSizeMsg{Width: w, Height: hgt})
		for _, screen := range []string{"conversation", "help"} {
			if screen == "help" {
				h.key("?")
			}
			lines := strings.Split(h.m.View(), "\n")
			if len(lines) != hgt {
				t.Errorf("%dx%d %s: %d lines", w, hgt, screen, len(lines))
			}
			for i, l := range lines {
				if got := ansi.StringWidth(l); got > w {
					t.Errorf("%dx%d %s: line %d is %d wide: %q", w, hgt, screen, i, got, ansi.Strip(l))
				}
			}
			if screen == "help" {
				h.key("?")
			}
		}
	}
}

// In a window too short for both, the transcript keeps its lines and the
// tasks' status lines give way.
func TestAShortWindowKeepsTheTranscriptOverTheStatusLines(t *testing.T) {
	h := opened(t, fixture())
	h.send(tea.WindowSizeMsg{Width: 80, Height: 7})
	h.has("● awaiting input", "• the timeout")

	h.send(tea.WindowSizeMsg{Width: 80, Height: 6})
	h.lacks("awaiting input")
	h.has("• the", "retry", "• the timeout")
}

func TestAWiderWindowRewrapsTheMessages(t *testing.T) {
	conv := fixture()
	conv.Messages[1].Content = "This reply is one paragraph that is long enough to need two lines at eighty columns, but only one at a hundred and sixty."
	h := opened(t, conv)
	countLines := func() int {
		n := 0
		for _, l := range h.lines() {
			if strings.Contains(l, "This reply") || strings.Contains(l, "sixty.") {
				n++
			}
		}
		return n
	}
	if countLines() != 2 {
		t.Fatalf("at 80 columns the paragraph's first and last words are on %d line(s), want 2:\n%s", countLines(), h.screen())
	}
	h.send(tea.WindowSizeMsg{Width: 160, Height: 24})
	if countLines() != 1 {
		t.Errorf("at 160 columns the paragraph's first and last words are on %d lines, want 1:\n%s", countLines(), h.screen())
	}
}

func TestALightBackgroundChangesTheMarkdownStyle(t *testing.T) {
	h := opened(t, fixture())
	dark := h.m.View()
	h.send(tea.BackgroundColorMsg{Color: lightColor{}})
	light := h.m.View()

	if dark == light {
		t.Error("the same styling on a light background as on a dark one")
	}
	if ansi.Strip(dark) != ansi.Strip(light) {
		t.Errorf("the text changed with the background:\n%s\n---\n%s", ansi.Strip(dark), ansi.Strip(light))
	}
}

// lightColor is white.
type lightColor struct{}

func (lightColor) RGBA() (r, g, b, a uint32) { return 0xffff, 0xffff, 0xffff, 0xffff }

// SetTitle renames the header, as Options.Title named it: one line of
// clean text. An empty one changes nothing.
func TestSetTitle(t *testing.T) {
	m := New(Options{ID: "c-1", Profile: "test", Title: "4f0c6d2e", Width: 80, Height: 24})
	m.SetTitle("cc-\x1b[31mhub\n2")
	want := strings.Split(ansi.Strip(m.View()), "\n")[0]
	if !strings.Contains(want, "loomux · test · cc-hub 2 ") {
		t.Errorf("header = %q", want)
	}
	m.SetTitle(" \n")
	if got := strings.Split(ansi.Strip(m.View()), "\n")[0]; got != want {
		t.Errorf("an empty title changed the header to %q", got)
	}
}

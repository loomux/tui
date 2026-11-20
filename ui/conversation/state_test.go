package conversation

import (
	"slices"
	"testing"
	"time"

	"github.com/Loomux/tui/client"
)

// t0 is the fixtures' noon; at(d) is d after it.
var t0 = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return t0.Add(d) }

func userMsg(id, dispatch, content string, d time.Duration) client.Message {
	return client.Message{ID: id, Role: client.RoleUser, Content: content, DispatchID: dispatch, CreatedAt: at(d)}
}

func replyMsg(id, dispatch, content string, d time.Duration) client.Message {
	return client.Message{ID: id, Role: client.RoleAssistant, Content: content, DispatchID: dispatch, CreatedAt: at(d)}
}

func msgIDs(ms []client.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// page is one answer of GET /conversations/{id}.
func page(hasMore bool, nextBefore string, ms ...client.Message) *client.Conversation {
	return &client.Conversation{ConversationID: "conv", Messages: ms, HasMore: hasMore, NextBefore: nextBefore}
}

func TestRefetchKeepsTheOlderPagesAlreadyLoaded(t *testing.T) {
	var s state
	s.applyNewest(page(true, "m3", userMsg("m3", "", "c", 3*time.Minute), replyMsg("m4", "", "d", 4*time.Minute)))
	s.applyOlder("m3", page(false, "", userMsg("m1", "", "a", time.Minute), replyMsg("m2", "", "b", 2*time.Minute)))
	// The newest page moved on by one message: it no longer holds m3.
	s.applyNewest(page(true, "m4", replyMsg("m4", "", "d", 4*time.Minute), userMsg("m5", "", "e", 5*time.Minute)))

	if got, want := msgIDs(s.messages), []string{"m1", "m2", "m3", "m4", "m5"}; !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
	if s.hasMore || s.nextBefore != "" {
		t.Errorf("hasMore = %v, nextBefore = %q: the oldest page was loaded, so nothing is older", s.hasMore, s.nextBefore)
	}
}

// A refetch whose page doesn't reach what is loaded leaves messages in
// between unknown: showing both as one transcript would hide the gap.
func TestRefetchThatDoesNotReachWhatIsLoadedStartsOverFromIt(t *testing.T) {
	var s state
	s.applyNewest(page(false, "", userMsg("m1", "", "a", time.Minute), replyMsg("m2", "", "b", 2*time.Minute)))
	s.applyNewest(page(true, "m7", userMsg("m7", "", "g", 7*time.Minute), replyMsg("m8", "", "h", 8*time.Minute)))

	if got, want := msgIDs(s.messages), []string{"m7", "m8"}; !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
	if !s.hasMore || s.nextBefore != "m7" {
		t.Errorf("hasMore = %v, nextBefore = %q, want the new page's (true, m7)", s.hasMore, s.nextBefore)
	}
}

func TestOlderPageGoesBeforeWhatIsLoaded(t *testing.T) {
	var s state
	s.applyNewest(page(true, "m3", userMsg("m3", "", "c", 3*time.Minute), replyMsg("m4", "", "d", 4*time.Minute)))
	if !s.applyOlder("m3", page(true, "m2", replyMsg("m2", "", "b", 2*time.Minute))) {
		t.Error("applyOlder = false for the page before the oldest message loaded")
	}

	if got, want := msgIDs(s.messages), []string{"m2", "m3", "m4"}; !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
	if !s.hasMore || s.nextBefore != "m2" {
		t.Errorf("hasMore = %v, nextBefore = %q, want the older page's (true, m2)", s.hasMore, s.nextBefore)
	}
}

func TestOlderPageThatOverlapsIsNotShownTwice(t *testing.T) {
	var s state
	s.applyNewest(page(true, "m3", userMsg("m3", "", "c", 3*time.Minute)))
	s.applyOlder("m3", page(false, "", userMsg("m2", "", "b", 2*time.Minute), userMsg("m3", "", "c", 3*time.Minute)))

	if got, want := msgIDs(s.messages), []string{"m2", "m3"}; !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
}

// An older page answers after a refetch replaced the transcript: it is
// the page before a message that is no longer the oldest one loaded.
func TestOlderPageForATranscriptSinceReplacedIsDropped(t *testing.T) {
	var s state
	s.applyNewest(page(true, "m3", userMsg("m3", "", "c", 3*time.Minute)))
	s.applyNewest(page(true, "m8", userMsg("m8", "", "h", 8*time.Minute)))

	if s.applyOlder("m3", page(false, "", userMsg("m2", "", "b", 2*time.Minute))) {
		t.Error("applyOlder = true for a page before m3, which isn't loaded any more")
	}
	if got, want := msgIDs(s.messages), []string{"m8"}; !slices.Equal(got, want) {
		t.Errorf("messages = %v, want %v", got, want)
	}
	if !s.hasMore || s.nextBefore != "m8" {
		t.Errorf("hasMore = %v, nextBefore = %q, want them unchanged (true, m8)", s.hasMore, s.nextBefore)
	}
}

func TestTaskUpdateChangesTheTaskItNames(t *testing.T) {
	var s state
	c := page(false, "")
	c.Tasks = []client.Task{
		{ID: "t1", Status: client.TaskCompleted, UpdatedAt: at(time.Minute)},
		{ID: "t2", Status: client.TaskNeedsAttention, Attention: &client.Attention{Title: "Bash command"}, UpdatedAt: at(2 * time.Minute)},
	}
	s.applyNewest(c)

	refetch := s.taskUpdate(client.TaskUpdate{TaskID: "t2", Status: client.TaskFailed, ErrorClass: "agent_rate_limited",
		FailureReason: "usage limit, resets 15:00", UpdatedAt: at(3 * time.Minute)})

	if !refetch {
		t.Error("taskUpdate = false for a change of status: the rest of the task comes with a fetch")
	}
	if got := s.tasks[0]; got.Status != client.TaskCompleted {
		t.Errorf("the other task changed: %+v", got)
	}
	got := s.tasks[1]
	if got.Status != client.TaskFailed || got.ErrorClass != "agent_rate_limited" || got.FailureReason != "usage limit, resets 15:00" || !got.UpdatedAt.Equal(at(3*time.Minute)) {
		t.Errorf("task = %+v, want it failed with the update's class, reason and time", got)
	}
	if got.Attention != nil {
		t.Errorf("attention = %+v: a task that isn't stopped at a prompt has none", got.Attention)
	}
}

// The stream repeats a task's state when it connects, and reports a
// task that was only touched: neither needs the conversation fetched.
func TestTaskUpdateWithTheSameStatusNeedsNoFetch(t *testing.T) {
	var s state
	c := page(false, "")
	c.Tasks = []client.Task{{ID: "t1", Status: client.TaskNeedsAttention, Attention: &client.Attention{Title: "Bash command"}, UpdatedAt: at(time.Minute)}}
	s.applyNewest(c)

	if s.taskUpdate(client.TaskUpdate{TaskID: "t1", Status: client.TaskNeedsAttention, UpdatedAt: at(2 * time.Minute)}) {
		t.Error("taskUpdate = true though the status didn't change")
	}
	got := s.tasks[0]
	if !got.UpdatedAt.Equal(at(2 * time.Minute)) {
		t.Errorf("updated_at = %v, want the event's", got.UpdatedAt)
	}
	if got.Attention == nil {
		t.Error("the prompt of a task still stopped at it is gone")
	}
}

// An event can arrive after a fetch that already had a later state: it
// isn't news.
func TestATaskUpdateOlderThanWhatIsKnownChangesNothing(t *testing.T) {
	var s state
	c := page(false, "")
	c.Tasks = []client.Task{{ID: "t1", Status: client.TaskAwaitingInput, UpdatedAt: at(5 * time.Minute)}}
	s.applyNewest(c)

	if s.taskUpdate(client.TaskUpdate{TaskID: "t1", Status: client.TaskRunning, UpdatedAt: at(4 * time.Minute)}) {
		t.Error("taskUpdate = true for an event older than the task's state")
	}
	if got := s.tasks[0]; got.Status != client.TaskAwaitingInput || !got.UpdatedAt.Equal(at(5*time.Minute)) {
		t.Errorf("task = %+v, want it as the fetch left it", got)
	}
}

// The stream may name a task the last fetch didn't have: the caller
// refetches, and until then the task shows with what the event says.
func TestTaskUpdateForAnUnknownTaskAddsIt(t *testing.T) {
	var s state
	refetch := s.taskUpdate(client.TaskUpdate{TaskID: "t9", WorkspaceID: "w1", Status: client.TaskRunning, UpdatedAt: at(time.Minute)})
	if !refetch {
		t.Error("taskUpdate = false for a task the conversation didn't have")
	}
	if len(s.tasks) != 1 || s.tasks[0].ID != "t9" || s.tasks[0].Status != client.TaskRunning || s.tasks[0].WorkspaceID != "w1" {
		t.Errorf("tasks = %+v, want t9 running", s.tasks)
	}
}

// A fetch that began before a stream event answers after it with the
// older state: it must not undo what the event said.
func TestAFetchOlderThanATaskUpdateDoesNotUndoIt(t *testing.T) {
	var s state
	first := page(false, "")
	first.Tasks = []client.Task{{ID: "t1", Status: client.TaskRunning, AgentType: "claude-code", UpdatedAt: at(time.Minute)}}
	s.applyNewest(first)
	s.taskUpdate(client.TaskUpdate{TaskID: "t1", Status: client.TaskAwaitingInput, UpdatedAt: at(2 * time.Minute)})

	stale := page(false, "")
	stale.Tasks = []client.Task{{ID: "t1", Status: client.TaskRunning, AgentType: "claude-code", UpdatedAt: at(time.Minute)}}
	s.applyNewest(stale)

	if got := s.tasks[0]; got.Status != client.TaskAwaitingInput || got.AgentType != "claude-code" {
		t.Errorf("task = %+v, want it still awaiting_input, with its agent type", got)
	}
}

func TestAFetchNewerThanATaskUpdateReplacesIt(t *testing.T) {
	var s state
	s.taskUpdate(client.TaskUpdate{TaskID: "t1", Status: client.TaskNeedsAttention, UpdatedAt: at(time.Minute)})
	fresh := page(false, "")
	fresh.Tasks = []client.Task{{ID: "t1", Status: client.TaskNeedsAttention, Attention: &client.Attention{Title: "Bash command"}, UpdatedAt: at(time.Minute)}}
	s.applyNewest(fresh)

	if got := s.tasks[0]; got.Attention == nil || got.Attention.Title != "Bash command" {
		t.Errorf("task = %+v, want the fetched one, with its prompt", got)
	}
}

func TestDispatchUpdateChangesTheDispatchItNames(t *testing.T) {
	var s state
	c := page(false, "")
	c.Dispatches = []client.Dispatch{{DispatchID: "d1", Status: client.DispatchRunning, CreatedAt: at(time.Minute)}}
	s.applyNewest(c)

	refetch := s.dispatchUpdate(client.DispatchUpdate{DispatchID: "d1", Status: client.DispatchSucceeded, Reply: "done"})

	if refetch {
		t.Error("dispatchUpdate = true for a dispatch the conversation has: the event carries all of the change")
	}
	if got := s.dispatches[0]; got.Status != client.DispatchSucceeded || got.Reply != "done" || !got.CreatedAt.Equal(at(time.Minute)) {
		t.Errorf("dispatch = %+v, want it succeeded with the reply, created when it was", got)
	}
}

func TestDispatchUpdateForAnUnknownDispatchAddsIt(t *testing.T) {
	var s state
	refetch := s.dispatchUpdate(client.DispatchUpdate{DispatchID: "d7", Status: client.DispatchQueued})
	if !refetch {
		t.Error("dispatchUpdate = false for a dispatch the conversation didn't have: its message isn't loaded")
	}
	if len(s.dispatches) != 1 || s.dispatches[0].DispatchID != "d7" || s.dispatches[0].Status != client.DispatchQueued {
		t.Errorf("dispatches = %+v, want d7 queued", s.dispatches)
	}
}

// The same holds for an event that arrives after a fetch already had the
// dispatch's end.
func TestADispatchUpdateDoesNotReopenADispatchThatEnded(t *testing.T) {
	var s state
	c := page(false, "")
	c.Dispatches = []client.Dispatch{{DispatchID: "d1", Status: client.DispatchSucceeded, Reply: "done", CreatedAt: at(time.Minute)}}
	s.applyNewest(c)

	s.dispatchUpdate(client.DispatchUpdate{DispatchID: "d1", Status: client.DispatchRunning})

	if got := s.dispatches[0]; got.Status != client.DispatchSucceeded || got.Reply != "done" {
		t.Errorf("dispatch = %+v, want it still succeeded with its reply", got)
	}
}

// A dispatch that ended never runs again, so a fetch that says it's
// running is older than the event that ended it.
func TestAFetchDoesNotReopenADispatchTheStreamEnded(t *testing.T) {
	var s state
	first := page(false, "")
	first.Dispatches = []client.Dispatch{{DispatchID: "d1", Status: client.DispatchRunning, CreatedAt: at(time.Minute)}}
	s.applyNewest(first)
	s.dispatchUpdate(client.DispatchUpdate{DispatchID: "d1", Status: client.DispatchFailed, Error: "the agent stopped", ErrorClass: "agent_api_error"})

	stale := page(false, "")
	stale.Dispatches = []client.Dispatch{{DispatchID: "d1", Status: client.DispatchRunning, CreatedAt: at(time.Minute)}}
	s.applyNewest(stale)

	if got := s.dispatches[0]; got.Status != client.DispatchFailed || got.Error != "the agent stopped" || got.ErrorClass != "agent_api_error" {
		t.Errorf("dispatch = %+v, want it still failed with its error", got)
	}
}

type wantItem struct {
	kind itemKind
	id   string // the message's id, or the dispatch's
}

func timelineOf(s *state) []wantItem {
	var out []wantItem
	for _, it := range s.timeline() {
		w := wantItem{kind: it.kind, id: it.dispatch.DispatchID}
		if it.kind == itemMessage {
			w.id = it.msg.ID
		}
		out = append(out, w)
	}
	return out
}

func TestTimelineIsTheMessagesInOrder(t *testing.T) {
	var s state
	c := page(false, "", userMsg("m1", "d1", "q", time.Minute), replyMsg("m2", "d1", "a", 2*time.Minute))
	c.Dispatches = []client.Dispatch{{DispatchID: "d1", Status: client.DispatchSucceeded, Reply: "a", CreatedAt: at(time.Minute)}}
	s.applyNewest(c)

	want := []wantItem{{itemMessage, "m1"}, {itemMessage, "m2"}}
	if got := timelineOf(&s); !slices.Equal(got, want) {
		t.Errorf("timeline = %v, want %v: the reply is shown once, as its message", got, want)
	}
}

func TestTimelineEndsWithTheTurnInFlight(t *testing.T) {
	for _, status := range []string{client.DispatchQueued, client.DispatchRunning} {
		var s state
		c := page(false, "", userMsg("m1", "d1", "q", time.Minute))
		c.Dispatches = []client.Dispatch{{DispatchID: "d1", Status: status, CreatedAt: at(time.Minute)}}
		s.applyNewest(c)

		want := []wantItem{{itemMessage, "m1"}, {itemInFlight, "d1"}}
		if got := timelineOf(&s); !slices.Equal(got, want) {
			t.Errorf("%s: timeline = %v, want %v", status, got, want)
		}
	}
}

// dispatch_update carries the reply; its message comes with the refetch
// message_added asks for. Until then the reply shows from the dispatch.
func TestTimelineShowsAReplyBeforeItsMessageIsFetched(t *testing.T) {
	var s state
	c := page(false, "", userMsg("m1", "d1", "q", time.Minute))
	c.Dispatches = []client.Dispatch{{DispatchID: "d1", Status: client.DispatchRunning, CreatedAt: at(time.Minute)}}
	s.applyNewest(c)
	s.dispatchUpdate(client.DispatchUpdate{DispatchID: "d1", Status: client.DispatchSucceeded, Reply: "the answer"})

	want := []wantItem{{itemMessage, "m1"}, {itemReply, "d1"}}
	if got := timelineOf(&s); !slices.Equal(got, want) {
		t.Errorf("timeline = %v, want %v", got, want)
	}
}

func TestTimelineShowsAFailedTurnAfterItsMessage(t *testing.T) {
	for _, status := range []string{client.DispatchFailed, client.DispatchInterrupted} {
		var s state
		c := page(false, "",
			userMsg("m1", "d1", "first", time.Minute),
			userMsg("m2", "d2", "second", 2*time.Minute), replyMsg("m3", "d2", "ok", 3*time.Minute))
		c.Dispatches = []client.Dispatch{
			{DispatchID: "d1", Status: status, Error: "boom", CreatedAt: at(time.Minute)},
			{DispatchID: "d2", Status: client.DispatchSucceeded, Reply: "ok", CreatedAt: at(2 * time.Minute)},
		}
		s.applyNewest(c)

		want := []wantItem{{itemMessage, "m1"}, {itemFailed, "d1"}, {itemMessage, "m2"}, {itemMessage, "m3"}}
		if got := timelineOf(&s); !slices.Equal(got, want) {
			t.Errorf("%s: timeline = %v, want %v", status, got, want)
		}
	}
}

// The dispatches of a conversation are always all of them, the messages
// one page: a turn older than the page isn't shown again at its end.
func TestTimelineLeavesOutTurnsOlderThanTheLoadedMessages(t *testing.T) {
	var s state
	c := page(true, "m8", userMsg("m8", "d8", "q", 8*time.Minute), replyMsg("m9", "d8", "a", 9*time.Minute))
	c.Dispatches = []client.Dispatch{
		{DispatchID: "d1", Status: client.DispatchSucceeded, Reply: "an old answer", CreatedAt: at(time.Minute)},
		{DispatchID: "d2", Status: client.DispatchFailed, Error: "an old failure", CreatedAt: at(2 * time.Minute)},
		{DispatchID: "d8", Status: client.DispatchSucceeded, Reply: "a", CreatedAt: at(8 * time.Minute)},
	}
	s.applyNewest(c)

	want := []wantItem{{itemMessage, "m8"}, {itemMessage, "m9"}}
	if got := timelineOf(&s); !slices.Equal(got, want) {
		t.Errorf("timeline = %v, want %v", got, want)
	}
}

// A turn first heard of on the stream ends before the refetch brings its
// message: it still shows, at the end.
func TestTimelineShowsATurnTheStreamEndedBeforeItsMessageIsFetched(t *testing.T) {
	var s state
	s.applyNewest(page(false, "", userMsg("m1", "", "earlier", time.Minute)))
	s.dispatchUpdate(client.DispatchUpdate{DispatchID: "d5", Status: client.DispatchFailed, Error: "no target"})

	want := []wantItem{{itemMessage, "m1"}, {itemFailed, "d5"}}
	if got := timelineOf(&s); !slices.Equal(got, want) {
		t.Errorf("timeline = %v, want %v", got, want)
	}
}

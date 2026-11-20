package conversation

import (
	"slices"

	"github.com/Loomux/tui/client"
)

// state is what the server has said about the conversation: the messages
// loaded so far (the newest page, and the older ones asked for since) and
// its tasks, dispatches and offers, which a fetch of the newest page
// gives complete.
// Fetches and the stream's events both change it, and they race: a fetch
// answers with the conversation as it was when it was asked.
type state struct {
	messages []client.Message // oldest first
	// hasMore says messages older than the first exist; nextBefore is
	// the cursor that fetches them.
	hasMore    bool
	nextBefore string

	tasks         []client.Task
	dispatches    []client.Dispatch
	confirmations []client.Confirmation
}

// applyNewest takes a fetch of the newest page. The older messages
// already loaded stay when the page reaches them. When it doesn't, the
// messages in between are unknown, so the page replaces what was loaded
// rather than joining it across a gap.
func (s *state) applyNewest(c *client.Conversation) {
	s.applyRest(c)
	var joined bool
	if s.messages, joined = joinNewest(s.messages, c.Messages, messageID); !joined {
		s.hasMore, s.nextBefore = c.HasMore, c.NextBefore
	}
}

// applyOlder takes the page asked for with the cursor before and puts it
// ahead of what is loaded. It reports false, and leaves the messages
// alone, when before is no longer the cursor of the oldest one loaded:
// the page belongs to a transcript that has since been replaced.
//
// It takes the page's messages only. The page is fetched beside the
// fetches of the newest page, not in turn with them, so its tasks,
// dispatches and offers may be older than what those have brought.
func (s *state) applyOlder(before string, c *client.Conversation) bool {
	if !s.hasMore || s.nextBefore != before {
		return false
	}
	s.messages = joinOlder(s.messages, c.Messages, messageID)
	s.hasMore, s.nextBefore = c.HasMore, c.NextBefore
	return true
}

func messageID(m client.Message) string { return m.ID }

// joinNewest folds the newest page of a list that grows at its end (a
// conversation's messages, a transcript's turns) into what is loaded of
// it. The items loaded before the page's first stay, and joined is true,
// when the page reaches them; otherwise the page is the list.
func joinNewest[T any](loaded, page []T, id func(T) string) (list []T, joined bool) {
	if len(page) > 0 {
		first := id(page[0])
		if i := slices.IndexFunc(loaded, func(x T) bool { return id(x) == first }); i > 0 {
			return append(slices.Clone(loaded[:i]), page...), true
		}
	}
	return page, false
}

// joinOlder puts the page before the oldest item loaded ahead of what is
// loaded, without the items both hold.
func joinOlder[T any](loaded, older []T, id func(T) string) []T {
	if len(loaded) > 0 {
		first := id(loaded[0])
		if i := slices.IndexFunc(older, func(x T) bool { return id(x) == first }); i >= 0 {
			older = older[:i]
		}
	}
	return append(slices.Clone(older), loaded...)
}

// applyRest takes a fetch's tasks, dispatches and offers. The fetch may
// be older than the stream's last word, so it never takes a task back to
// an earlier state or reopens a dispatch that ended.
func (s *state) applyRest(c *client.Conversation) {
	tasks := slices.Clone(c.Tasks)
	for i, t := range tasks {
		if j := indexTask(s.tasks, t.ID); j >= 0 && s.tasks[j].UpdatedAt.After(t.UpdatedAt) {
			tasks[i] = s.tasks[j]
		}
	}
	dispatches := slices.Clone(c.Dispatches)
	for i, d := range dispatches {
		if j := indexDispatch(s.dispatches, d.DispatchID); j >= 0 && s.dispatches[j].Done() && !d.Done() {
			dispatches[i] = s.dispatches[j]
		}
	}
	s.tasks, s.dispatches, s.confirmations = tasks, dispatches, c.Confirmations
}

// taskUpdate applies a task_update event. It reports whether the
// conversation should be fetched for what the event doesn't carry: the
// task is new here, or its status changed (a prompt, an output tail and
// new messages come with a change of status).
func (s *state) taskUpdate(u client.TaskUpdate) (refetch bool) {
	i := indexTask(s.tasks, u.TaskID)
	if i < 0 {
		s.tasks = append(s.tasks, client.Task{ID: u.TaskID, WorkspaceID: u.WorkspaceID, Status: u.Status,
			ErrorClass: u.ErrorClass, FailureReason: u.FailureReason, UpdatedAt: u.UpdatedAt})
		return true
	}
	t := &s.tasks[i]
	if u.UpdatedAt.Before(t.UpdatedAt) {
		return false
	}
	refetch = t.Status != u.Status
	t.Status, t.ErrorClass, t.FailureReason, t.UpdatedAt = u.Status, u.ErrorClass, u.FailureReason, u.UpdatedAt
	if t.Status != client.TaskNeedsAttention {
		t.Attention = nil
	}
	return refetch
}

// dispatchUpdate applies a dispatch_update event. It reports whether the
// conversation should be fetched: the dispatch is new here, so its
// message isn't loaded either.
func (s *state) dispatchUpdate(u client.DispatchUpdate) (refetch bool) {
	i := indexDispatch(s.dispatches, u.DispatchID)
	if i < 0 {
		s.dispatches = append(s.dispatches, client.Dispatch{DispatchID: u.DispatchID})
		i, refetch = len(s.dispatches)-1, true
	}
	d := &s.dispatches[i]
	if d.Done() {
		return refetch
	}
	d.Status, d.Reply, d.Error, d.ErrorClass = u.Status, u.Reply, u.Error, u.ErrorClass
	return refetch
}

// itemKind is what one entry of the transcript is.
type itemKind int

const (
	itemMessage  itemKind = iota // a message
	itemReply                    // a turn's reply, known from its dispatch before its message is fetched
	itemInFlight                 // a turn in flight: its dispatch is queued or running
	itemFailed                   // a turn that failed or was interrupted
)

// item is one entry of the transcript: msg for a message, dispatch for
// the rest.
type item struct {
	kind     itemKind
	msg      client.Message
	dispatch client.Dispatch
}

// timeline is the transcript as shown: the loaded messages in order, a
// failed turn after the message that asked for it, and at the end what
// the dispatches say that no loaded message does (a turn in flight, a
// reply or a failure not fetched yet).
func (s *state) timeline() []item {
	byID := make(map[string]client.Dispatch, len(s.dispatches))
	for _, d := range s.dispatches {
		byID[d.DispatchID] = d
	}
	replied := map[string]bool{} // its reply is a loaded message
	placed := map[string]bool{}  // its failure follows its message
	var out []item
	for _, m := range s.messages {
		out = append(out, item{kind: itemMessage, msg: m})
		d, ok := byID[m.DispatchID]
		switch {
		case !ok:
		case m.Role == client.RoleAssistant:
			replied[d.DispatchID] = true
		case ended(d) && !placed[d.DispatchID]:
			out = append(out, item{kind: itemFailed, dispatch: d})
			placed[d.DispatchID] = true
		}
	}
	for _, d := range s.dispatches {
		switch {
		case !d.Done():
			out = append(out, item{kind: itemInFlight, dispatch: d})
		case !s.current(d):
			// Its messages are on an older page.
		case d.Status == client.DispatchSucceeded:
			if d.Reply != "" && !replied[d.DispatchID] {
				out = append(out, item{kind: itemReply, dispatch: d})
			}
		case !placed[d.DispatchID]:
			out = append(out, item{kind: itemFailed, dispatch: d})
		}
	}
	return out
}

// ended reports whether a dispatch ended without a reply: it failed or
// was interrupted.
func ended(d client.Dispatch) bool {
	return d.Done() && d.Status != client.DispatchSucceeded
}

// current reports whether d is no older than the loaded messages, so
// that a message of its turn would be among them. A dispatch first heard
// of on the stream has no creation time yet and is current.
func (s *state) current(d client.Dispatch) bool {
	return d.CreatedAt.IsZero() || len(s.messages) == 0 || !d.CreatedAt.Before(s.messages[0].CreatedAt)
}

func indexTask(ts []client.Task, id string) int {
	return slices.IndexFunc(ts, func(t client.Task) bool { return t.ID == id })
}

func indexDispatch(ds []client.Dispatch, id string) int {
	return slices.IndexFunc(ds, func(d client.Dispatch) bool { return d.DispatchID == id })
}

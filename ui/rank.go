package ui

import (
	"cmp"
	"slices"
	"strings"

	"github.com/Loomux/tui/client"
)

// A group is one of the list's sections, in rank order.
type group int

const (
	needsYou group = iota // waiting on the user: a prompt, an idle agent, a hold or an offer
	running               // a turn in flight
	done                  // completed, failed, or a status this client doesn't know
)

func (g group) title() string {
	switch g {
	case needsYou:
		return "NEEDS YOU"
	case running:
		return "RUNNING"
	}
	return "DONE"
}

// groupOf ranks a conversation as the web does (src/lib/conversations.ts),
// by its latest task's status, plus a pending offer (S6), which needs
// the user whatever the status.
func groupOf(r client.ConversationSummary) group {
	if r.PendingConfirmationID != "" {
		return needsYou
	}
	switch r.Status {
	case client.TaskNeedsAttention, client.TaskAwaitingInput, client.TaskHumanTakeover:
		return needsYou
	case client.TaskRunning:
		return running
	}
	return done
}

// rank sorts rows attention-first: by group, then the most recently
// updated first, then by conversation_id.
func rank(rows []client.ConversationSummary) {
	slices.SortStableFunc(rows, func(a, b client.ConversationSummary) int {
		if c := cmp.Compare(groupOf(a), groupOf(b)); c != 0 {
			return c
		}
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ConversationID, b.ConversationID)
	})
}

// A view is which groups the list shows; tab (next_group) cycles them.
type view int

const (
	allView view = iota
	needsYouView
	runningView
	doneView
)

func (v view) next() view { return (v + 1) % 4 }

func (v view) shows(g group) bool {
	switch v {
	case needsYouView:
		return g == needsYou
	case runningView:
		return g == running
	case doneView:
		return g == done
	}
	return true
}

// matches reports whether the row has every word of the query, in any
// case, in what it shows (preview, workspace, status, holder) or its id.
func (it item) matches(query string) bool {
	hay := strings.ToLower(strings.Join([]string{it.preview, it.workspace, it.label, it.ConversationID}, "\n"))
	for _, w := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

package ui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/Loomux/tui/client"
)

// An item is a row as the list shows it.
type item struct {
	client.ConversationSummary
	group     group
	glyph     string // the status glyph
	label     string // the status, holder or offer, in words
	workspace string // the workspace's name, or a short id while it isn't known
	preview   string // the first message, on one line
}

// newItem makes a row's item; names maps workspace ids to names.
func newItem(r client.ConversationSummary, names map[string]string) item {
	it := item{ConversationSummary: r, group: groupOf(r), preview: clean(r.Preview)}
	it.glyph, it.label = status(r)
	if r.Watchers > 0 {
		it.label += fmt.Sprintf(", %d watching", r.Watchers)
	}
	it.workspace = workspaceName(r.WorkspaceID, names)
	return it
}

// status is a row's glyph and words: who holds it, then a pending
// offer, then its status.
func status(r client.ConversationSummary) (string, string) {
	if r.HeldBy != nil {
		if who := holderName(r.HeldBy); who != "" {
			return "⌨", "held: " + who
		}
		return "⌨", "held"
	}
	if r.PendingConfirmationID != "" {
		return "?", "offer"
	}
	switch r.Status {
	case client.TaskNeedsAttention:
		return "▲", "needs attention"
	case client.TaskAwaitingInput:
		return "●", "awaiting input"
	case client.TaskHumanTakeover:
		return "⌨", "taken over"
	case client.TaskRunning:
		return "◐", "running"
	case client.TaskCompleted:
		return "✓", "completed"
	case client.TaskFailed:
		return "✗", "failed"
	}
	return "·", strings.ReplaceAll(clean(r.Status), "_", " ")
}

// holderName names who holds a conversation: the client's label, else
// its kind, else "".
func holderName(h *client.Holder) string {
	if h == nil {
		return ""
	}
	if who := clean(h.ClientLabel); who != "" {
		return who
	}
	return clean(h.ClientKind)
}

// workspaceName is a workspace's name, the start of its id while the
// name isn't known, or "—" for a conversation with no task.
func workspaceName(id string, names map[string]string) string {
	if id == "" {
		return "—"
	}
	if n := clean(names[id]); n != "" {
		return n
	}
	if r := []rune(clean(id)); len(r) > 8 {
		return string(r[:8])
	}
	return clean(id)
}

// clean puts text from the server on one line for a row: whitespace runs
// become one space, and control characters (an escape sequence's ESC
// among them) and bidi controls are dropped, so a message can't move
// the cursor, restyle the terminal or reorder what a row shows. Invalid
// UTF-8 becomes U+FFFD.
func clean(s string) string {
	var sb strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = true
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
		default:
			if space && sb.Len() > 0 {
				sb.WriteByte(' ')
			}
			space = false
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

package ui

// Handing work between clients (the design's workflow 5): o opens the
// conversation on the web, T takes it back from the terminal driving it,
// and loomux open <id> starts on a conversation.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
	"github.com/Loomux/tui/internal/browser"
)

// takeBackTimeout bounds a take back: the server detaches the holder's
// terminal before it answers.
const takeBackTimeout = 30 * time.Second

// browsedMsg is how opening a link in the browser went.
type browsedMsg struct {
	link string
	err  error
}

// takeBackMsg is a take back's answer: the releases, in order, until an
// error.
type takeBackMsg struct {
	who      string // the holder, as the row named it
	released []*client.Release
	err      error
}

// webLink is a conversation's page on the web.
func webLink(server, id string) string {
	return server + "/conversations/" + url.PathEscape(id)
}

// browse opens the conversation under the cursor in the browser.
func (a *App) browse() tea.Cmd {
	it, ok := a.home.current()
	if !ok {
		return nil
	}
	link, open := webLink(a.server, it.ConversationID), a.browseFn
	return func() tea.Msg { return browsedMsg{link, open(link)} }
}

// gotBrowsed says how it went, the link first, so a narrow footer cuts
// the words rather than the link to copy.
func (a *App) gotBrowsed(msg browsedMsg) {
	switch {
	case msg.err == nil:
		a.notice = msg.link + "  opened in the browser"
	case errors.Is(msg.err, browser.ErrNoBrowser):
		a.notice = msg.link + "  (no browser here: copy the link)"
	default:
		a.notice = msg.link + "  (couldn't open a browser: " + clean(msg.err.Error()) + ")"
	}
}

// openAtStart opens the conversation loomux open named, once the first
// list is in.
func (a *App) openAtStart() tea.Cmd {
	id := a.opening
	a.opening = ""
	if !a.home.selectID(id) {
		a.notice = fmt.Sprintf("%q isn't in the list (the newest %d conversations)", clean(id), pollLimit)
		return nil
	}
	return a.openConversation()
}

// isHeld reports whether a row is driven from a terminal: the server says
// who holds it (S6), or, from a server that doesn't, its status says it
// is taken over.
func isHeld(it item) bool {
	return it.HeldBy != nil || it.Status == client.TaskHumanTakeover
}

// takeBack takes the conversation under the cursor back from the
// terminal driving it: each of its held tasks is released without a hold
// id, which is the API's Take back. The server detaches the holder's
// terminal and hands the task back.
func (a *App) takeBack() tea.Cmd {
	it, ok := a.home.current()
	switch {
	case !ok:
		return nil
	case a.takingBack:
		a.notice = "taking it back…"
		return nil
	case !isHeld(it):
		a.notice = "nothing to take back: it isn't driven from a terminal"
		return nil
	}
	a.takingBack = true
	a.notice = "taking it back…"
	api, id, who := a.api, it.ConversationID, holderName(it.HeldBy)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), takeBackTimeout)
		defer cancel()
		conv, err := api.Conversation(ctx, id, client.Page{Limit: 1})
		if err != nil {
			return takeBackMsg{who: who, err: err}
		}
		msg := takeBackMsg{who: who}
		for _, t := range heldTasks(conv.Tasks) {
			r, err := api.Release(ctx, t.ID, "")
			if err != nil {
				msg.err = err
				break
			}
			msg.released = append(msg.released, r)
		}
		return msg
	}
}

// heldTasks are the tasks a take back releases: those with a drive hold,
// or, from a server that sends no holds (before S6), those in
// human_takeover. A watch hold isn't taken back.
func heldTasks(tasks []client.Task) []client.Task {
	var out []client.Task
	holds := false
	for _, t := range tasks {
		if t.Hold != nil {
			holds = true
			if t.Hold.Mode != client.ModeWatch {
				out = append(out, t)
			}
		}
	}
	if holds {
		return out
	}
	for _, t := range tasks {
		if t.Status == client.TaskHumanTakeover {
			out = append(out, t)
		}
	}
	return out
}

// gotTakeBack says how the take back went and polls the list at once.
func (a *App) gotTakeBack(msg takeBackMsg) tea.Cmd {
	a.takingBack = false
	switch {
	case msg.err != nil:
		a.notice = "couldn't take it back: " + clean(msg.err.Error())
	case len(msg.released) == 0:
		a.notice = "nothing to take back: it has been given back already"
	default:
		last := msg.released[len(msg.released)-1]
		who := msg.who
		if last.Hold != nil {
			if n := holderName(&client.Holder{ClientKind: last.Hold.ClientKind, ClientLabel: last.Hold.ClientLabel}); n != "" {
				who = n
			}
		}
		a.notice = "took it back"
		if who != "" {
			a.notice += " from " + who
		}
		_, words := status(client.ConversationSummary{Status: last.Status})
		if n := len(msg.released); n > 1 {
			a.notice += fmt.Sprintf(": %d tasks, the last %s", n, words)
		} else {
			a.notice += ": the task is " + words
		}
	}
	if a.signedOut {
		return nil
	}
	return a.poll()
}

// heldSince is when a hold began, in now's time zone: the time today,
// the date and time before.
func heldSince(since, now time.Time) string {
	if since.IsZero() {
		return ""
	}
	since = since.In(now.Location())
	if y, m, d := since.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return "since " + since.Format("15:04")
	}
	return "since " + since.Format("Jan 2 15:04")
}

// takeBackHint is the footer's lead on a held row: the key, from whom,
// and since when.
func (a *App) takeBackHint(it item) string {
	k := a.firstKey(keymap.TakeBack)
	if k == "" {
		return ""
	}
	hint := bold.Render(k) + " take back"
	if it.HeldBy == nil {
		return hint
	}
	if who := holderName(it.HeldBy); who != "" {
		hint += " from " + who
	}
	if s := heldSince(it.HeldBy.Since, a.now()); s != "" {
		hint += " (" + s + ")"
	}
	return hint
}

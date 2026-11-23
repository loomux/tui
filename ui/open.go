package ui

// The conversation opened over the list (package conversation): enter,
// or loomux open, opens it, and back returns to the list. The list goes
// on behind it: its polls, and whatever an action of its own has still
// to answer.

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/tui/config/keymap"
	"github.com/Loomux/tui/ui/conversation"
)

// openConversation opens the conversation under the cursor, for enter
// and loomux open.
func (a *App) openConversation() tea.Cmd {
	it, ok := a.home.current()
	switch {
	case !ok || a.signedOut:
		return nil // signed out, the rows aren't shown
	case a.starting != nil && !a.starting.cancelled:
		// It is about to take the terminal, or to ask a question.
		a.notice = "attaching…"
		return nil
	}
	// The filter's input or the help, opened before the list came in,
	// doesn't wait under the conversation.
	a.filter.Blur()
	a.screen = keymap.ListScreen
	a.convID = it.ConversationID
	a.conv = conversation.New(conversation.Options{
		Client: a.api, ID: a.convID, Profile: a.profile, Title: it.workspace,
		Keys: a.keys, Width: a.width, Height: a.height, Now: a.now,
	})
	return a.conv.Init()
}

// updateConversation gives the open conversation a message.
func (a *App) updateConversation(msg tea.Msg) tea.Cmd {
	if a.conv == nil {
		return nil
	}
	_, cmd := a.conv.Update(msg)
	return cmd
}

// leaveConversation is back: the list again, polled at once (the design:
// "right after any action").
func (a *App) leaveConversation() tea.Cmd {
	if a.conv == nil {
		return nil // a 401 closed it first
	}
	a.closeConversation()
	return Refresh
}

func (a *App) closeConversation() {
	if a.conv != nil {
		a.conv.Close()
		a.conv = nil
	}
}

// nameConversation puts the open conversation's workspace in its header,
// once it is known: loomux open may open one before the names have come,
// and a conversation may get its workspace while it is open.
func (a *App) nameConversation() {
	if a.conv == nil {
		return
	}
	for _, it := range a.home.items {
		if it.ConversationID == a.convID {
			a.conv.SetTitle(it.workspace)
		}
	}
}

// conversationView is the open conversation's window, with the list's
// notice, when there is one, in its footer's place.
func (a *App) conversationView() string {
	view := a.conv.View()
	if a.notice == "" {
		return view
	}
	lines := strings.Split(view, "\n")
	lines[len(lines)-1] = fit(" "+a.notice, a.width)
	return strings.Join(lines, "\n")
}

package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/tui/client"
)

// poller is the list's polling state.
type poller struct {
	gen       int  // the poll a timer's tick is for: a newer poll makes older ticks stale
	polling   bool // a poll is in flight
	again     bool // poll again as soon as the one in flight is answered
	loaded    bool // a poll has been answered
	lastOK    time.Time
	pollErr   error // the last poll's error, until one succeeds
	signedOut bool  // a 401: polling has stopped

	names     map[string]string // workspace id → name
	namesBusy bool
	namesAt   time.Time // when the names were last asked for
}

// pageMsg is a poll's answer.
type pageMsg struct {
	page *client.ConversationPage
	err  error
}

// namesMsg is GET /workspaces's answer.
type namesMsg struct {
	workspaces []client.Workspace
	err        error
}

// pollMsg is the timer's tick for poll gen.
type pollMsg struct{ gen int }

type refreshMsg struct{}

// Refresh polls the list at once, as the design asks right after any
// action. It is a tea.Cmd: return it from an action's update.
func Refresh() tea.Msg { return refreshMsg{} }

// poll starts a poll, or, with one in flight, asks for another as soon as
// it is answered.
func (a *App) poll() tea.Cmd {
	if a.polling {
		a.again = true
		return nil
	}
	a.polling = true
	a.gen++
	api := a.api
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		p, err := api.Conversations(ctx, pollLimit)
		return pageMsg{p, err}
	}
}

func (a *App) gotPage(msg pageMsg) tea.Cmd {
	a.polling = false
	if msg.err != nil {
		a.pollErr = msg.err
		if client.Unauthorized(msg.err) {
			a.signOut(msg.err)
			return nil
		}
	} else {
		a.pollErr = nil
		a.loaded = true
		a.lastOK = a.now()
		a.home.setRows(msg.page.Conversations, msg.page.HasMore, a.names)
		a.nameConversation()
	}
	if a.signedOut {
		return nil // an open conversation's 401 came first: this was the last poll
	}
	var cmds []tea.Cmd
	if a.opening != "" && msg.err == nil {
		cmds = append(cmds, a.openAtStart())
	}
	if a.again {
		a.again = false
		cmds = append(cmds, a.poll())
	} else {
		gen := a.gen
		cmds = append(cmds, a.tick(pollEvery, func(time.Time) tea.Msg { return pollMsg{gen} }))
	}
	if since := a.now().Sub(a.namesAt); msg.err == nil && !a.namesBusy &&
		(since >= namesStale || since >= namesEvery && a.lacksName(msg.page.Conversations)) {
		cmds = append(cmds, a.fetchNames())
	}
	return tea.Batch(cmds...)
}

// signOut is the answer to a 401, the list's or an open conversation's:
// the token is no good and asking again won't change that. Polling
// stops, the conversation closes, and the list says to sign in again.
func (a *App) signOut(err error) {
	a.signedOut, a.pollErr = true, err
	a.closeConversation()
}

func (a *App) lacksName(rows []client.ConversationSummary) bool {
	for _, r := range rows {
		if _, ok := a.names[r.WorkspaceID]; r.WorkspaceID != "" && !ok {
			return true
		}
	}
	return false
}

func (a *App) fetchNames() tea.Cmd {
	a.namesBusy = true
	a.namesAt = a.now()
	api := a.api
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		ws, err := api.Workspaces(ctx)
		return namesMsg{ws, err}
	}
}

// gotNames takes the workspaces' names. Without them a row shows the
// start of its workspace's id, so a failure is no error on screen. An
// attach waiting to name its window goes on: with the name if it came,
// else with what it had.
func (a *App) gotNames(msg namesMsg) tea.Cmd {
	a.namesBusy = false
	if msg.err == nil {
		names := make(map[string]string, len(msg.workspaces))
		for _, w := range msg.workspaces {
			names[w.ID] = w.Name
		}
		a.names = names
		a.home.setNames(names)
		a.nameConversation()
	}
	st := a.starting
	if st == nil || !st.waitName {
		return nil
	}
	st.waitName = false
	if st.cancelled {
		a.settle(st)
		return nil
	}
	if st.ws != "" && a.names[st.ws] != "" {
		st.name = workspaceName(st.ws, a.names)
	}
	return a.attachNow(st)
}

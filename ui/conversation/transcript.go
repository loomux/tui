package conversation

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
)

// transcript is a task's transcript (GET /tasks/{id}/transcript): each
// turn's message to the agent, the agent's answer and its pane as the
// turn ended. It is shown over the conversation, without attaching, and
// back returns to it.
type transcript struct {
	task client.Task
	// seq tells this opening's answers from an earlier one's.
	seq int

	turns      []client.Turn // oldest first
	hasMore    bool
	nextBefore string

	// As the conversation's: one fetch of the newest page at a time, and
	// one more when the task changed while it was in flight.
	loaded        bool
	fetching      bool
	stale         bool
	err           error
	olderFetching bool
	olderErr      error

	body   viewport.Model
	follow bool
}

// turnsMsg is a transcript fetch's answer.
type turnsMsg struct {
	inst   uint64
	seq    int
	page   *client.Transcript
	err    error
	older  bool
	before string
}

// openTranscript shows the transcript of the conversation's most
// recently updated task: the one the stream reports on.
func (m *Model) openTranscript() tea.Cmd {
	if m.signedOut {
		return nil
	}
	if len(m.st.tasks) == 0 {
		m.notice = "this conversation has no task yet"
		return nil
	}
	task := m.st.tasks[0]
	for _, t := range m.st.tasks[1:] {
		if t.UpdatedAt.After(task.UpdatedAt) {
			task = t
		}
	}
	m.trSeq++
	m.tr = &transcript{task: task, seq: m.trSeq, body: viewport.New(), follow: true}
	cmd := m.fetchTurns()
	m.render()
	return cmd
}

// fetchTurns asks for the newest page of turns (the server's page size),
// one fetch at a time.
func (m *Model) fetchTurns() tea.Cmd {
	tr := m.tr
	if tr.fetching {
		tr.stale = true
		return nil
	}
	tr.fetching, tr.stale = true, false
	return m.getTurns(client.Page{}, false)
}

func (m *Model) fetchOlderTurns() tea.Cmd {
	tr := m.tr
	if !tr.hasMore || tr.olderFetching || m.signedOut {
		return nil
	}
	tr.olderFetching, tr.olderErr = true, nil
	m.render()
	return m.getTurns(client.Page{Before: tr.nextBefore}, true)
}

func (m *Model) getTurns(page client.Page, older bool) tea.Cmd {
	parent, api, inst, seq, task := m.ctx, m.api, m.inst, m.tr.seq, m.tr.task.ID
	return func() tea.Msg {
		ctx, cancel := contextWithTimeout(parent)
		defer cancel()
		p, err := api.Transcript(ctx, task, page)
		return turnsMsg{inst: inst, seq: seq, page: p, err: err, older: older, before: page.Before}
	}
}

func (m *Model) onTurns(msg turnsMsg) tea.Cmd {
	tr := m.tr
	if tr == nil || msg.seq != tr.seq {
		return nil // the answer of a transcript since left
	}
	if client.Unauthorized(msg.err) {
		return m.signOut(msg.err)
	}
	if msg.older {
		tr.olderFetching, tr.olderErr = false, msg.err
		if msg.err != nil {
			m.render()
			return nil
		}
		total, offset := tr.body.TotalLineCount(), tr.body.YOffset()
		joined := tr.hasMore && tr.nextBefore == msg.before
		if joined {
			tr.turns = joinOlder(tr.turns, msg.page.Turns, turnID)
			tr.hasMore, tr.nextBefore = msg.page.HasMore, msg.page.NextBefore
		}
		m.render()
		if joined && !tr.follow {
			tr.body.SetYOffset(offset + tr.body.TotalLineCount() - total)
		}
		return nil
	}
	tr.fetching = false
	if tr.err = msg.err; msg.err == nil {
		tr.loaded = true
		var joined bool
		if tr.turns, joined = joinNewest(tr.turns, msg.page.Turns, turnID); !joined {
			tr.hasMore, tr.nextBefore = msg.page.HasMore, msg.page.NextBefore
		}
	}
	m.render()
	if tr.stale {
		return m.fetchTurns()
	}
	return nil
}

func turnID(t client.Turn) string { return t.ID }

func (m *Model) onTranscriptKey(msg tea.KeyPressMsg) tea.Cmd {
	tr := m.tr
	action, _, ok := m.keys.Lookup(keymap.TranscriptScreen, msg.String())
	if !ok {
		return nil
	}
	if moved, top := scroll(&tr.body, action); moved {
		tr.follow = tr.body.AtBottom()
		if top {
			return m.fetchOlderTurns()
		}
		return nil
	}
	switch action {
	case keymap.Back:
		m.tr = nil
		m.render()
	case keymap.Quit:
		m.Close()
		return tea.Quit
	case keymap.Help:
		m.openHelp()
	}
	return nil
}

// transcriptContent is the transcript's lines.
func (m *Model) transcriptContent() []string {
	tr := m.tr
	switch {
	case !tr.loaded && tr.err != nil:
		return paint(bad, indent(" ", wrap("Couldn't load the transcript: "+errText(tr.err), m.width-2)))
	case !tr.loaded:
		return []string{faint.Render(" loading…")}
	}
	var out []string
	switch {
	case tr.olderFetching:
		out = append(out, faint.Render(" loading older turns…"), "")
	case tr.olderErr != nil:
		out = append(out, paint(bad, indent(" ", wrap("⚠ couldn't load older turns: "+errText(tr.olderErr), m.width-2)))...)
		out = append(out, "")
	case tr.hasMore:
		out = append(out, faint.Render(" ↑ scroll up for older turns"), "")
	}
	if len(tr.turns) == 0 {
		out = append(out, faint.Render(" no turns yet"))
	}
	for i, t := range tr.turns {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, m.cached("turn "+t.ID, func() []string { return m.turnLines(t) })...)
	}
	return out
}

// turnLines is one turn: when it ended, what was sent to the agent, the
// agent's answer (markdown), and the pane as it stood.
func (m *Model) turnLines(t client.Turn) []string {
	head := " ── " + m.clock(t.CreatedAt) + " "
	out := []string{faint.Render(head + strings.Repeat("─", max(m.width-1-ansi.StringWidth(head), 0)))}
	width := m.width - len(bodyIndent) - 1
	section := func(name string, body []string) {
		if len(body) > 0 {
			out = append(out, " "+strong.Render(name))
			out = append(out, body...)
		}
	}
	if sent := wrap(strings.TrimSpace(clean(t.UserMessage)), width); sent != "" {
		section("sent", indent(bodyIndent, sent))
	}
	if answer := m.md.render(t.AgentMessage, width); answer != "" {
		section("answer", indent(bodyIndent, answer))
	}
	section("pane", paneLines(t.Pane, width))
	return out
}

// paneLines is a captured pane as text: no line cut (one wider than the
// window continues on the next), nothing after its last line.
func paneLines(pane string, width int) []string {
	text := strings.TrimRight(clean(pane), " \n")
	if text == "" {
		return nil
	}
	var out []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, " ")
		for _, part := range strings.Split(ansi.Hardwrap(l, max(width-2, 1), true), "\n") {
			out = append(out, bodyIndent+faint.Render("│ "+part))
		}
	}
	return out
}

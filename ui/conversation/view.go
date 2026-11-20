package conversation

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
)

// The colours are the terminal's own, so the screen follows its theme.
// A status is drawn as the list draws it: its glyph, its words and its
// colour.
var (
	faint   = lipgloss.NewStyle().Faint(true)
	strong  = lipgloss.NewStyle().Bold(true)
	youName = strong.Foreground(lipgloss.Cyan)
	theirs  = strong.Foreground(lipgloss.Magenta)
	good    = lipgloss.NewStyle().Foreground(lipgloss.Green)
	busy    = lipgloss.NewStyle().Foreground(lipgloss.Blue)
	warn    = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	needs   = warn.Bold(true)
	bad     = lipgloss.NewStyle().Foreground(lipgloss.Red)
)

const (
	// bodyIndent is how far a message's text stands in from its name.
	bodyIndent = "   "
	// minBody is the fewest transcript lines the task lines leave: in a
	// window too short for both, the transcript wins.
	minBody = 3
	// maxStatus is the most task lines shown.
	maxStatus = 3
)

// View is the whole window: the header, the transcript, the tasks'
// status lines and the footer. It is empty until the size is known.
func (m *Model) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	lines := []string{m.header()}
	if m.height >= 3 {
		switch {
		case m.help:
			lines = append(lines, viewLines(m.helpBody)...)
		case m.tr != nil:
			lines = append(lines, viewLines(m.tr.body)...)
		default:
			lines = append(lines, viewLines(m.body)...)
			if len(m.status) > 0 {
				lines = append(lines, faint.Render(strings.Repeat("─", m.width)))
				lines = append(lines, m.status...)
			}
		}
	}
	if m.height >= 2 {
		lines = append(lines, m.footer())
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "")
	}
	return strings.Join(lines, "\n")
}

func viewLines(v viewport.Model) []string {
	if v.Height() <= 0 {
		return nil
	}
	return strings.Split(v.View(), "\n")
}

// render lays the screen out again after a change: how the window is
// divided, and what each part holds.
func (m *Model) render() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	room := max(m.height-2, 0) // between the header and the footer
	m.status = m.statusLines()
	extra := 0
	if len(m.status) > 0 {
		extra = len(m.status) + 1 // and the rule above them
	}
	if room-extra < minBody {
		m.status, extra = nil, 0
	}
	m.body.SetWidth(m.width)
	m.body.SetHeight(room - extra)
	m.body.SetContentLines(m.content())
	if m.follow {
		m.body.GotoBottom()
	}
	m.helpBody.SetWidth(m.width)
	m.helpBody.SetHeight(room)
	m.helpBody.SetContentLines(m.helpLines())
	if tr := m.tr; tr != nil {
		tr.body.SetWidth(m.width)
		tr.body.SetHeight(room)
		tr.body.SetContentLines(m.transcriptContent())
		if tr.follow {
			tr.body.GotoBottom()
		}
	}
}

// header is the first line: where this is, as the list's header says it,
// then the stream's state and the help key.
func (m *Model) header() string {
	left := " " + strong.Render("loomux")
	parts := []string{m.profile, m.title}
	if m.tr != nil {
		parts = append(parts, "transcript of "+cmp.Or(line(m.tr.task.AgentType), line(m.tr.task.Kind), "the task"))
	}
	for _, part := range parts {
		if part != "" {
			left += " · " + part
		}
	}
	right := m.streamState() + "   " + m.keys.Keys(keymap.Help)[0] + "=help"
	return row(left, right, m.width)
}

// streamState is what the header says of the screen's link to the
// server: an error first, then the stream.
func (m *Model) streamState() string {
	problem := func(text string) string {
		return bad.Render("⚠ " + ansi.Truncate(text, max(m.width/2, 10), "…"))
	}
	switch {
	case m.signedOut:
		return problem("signed out")
	case m.tr != nil && m.tr.err != nil && m.tr.loaded:
		return problem(errText(m.tr.err))
	case m.err != nil && m.loaded:
		return problem(errText(m.err))
	case m.streamErr != nil:
		return problem(errText(m.streamErr))
	case m.reconnecting:
		return warn.Render("reconnecting…")
	case m.connected:
		return good.Render("● live")
	}
	return faint.Render("connecting…")
}

// footer is the last line: the keys, or what there is to say instead.
func (m *Model) footer() string {
	switch {
	case m.signedOut:
		return warn.Render(" The server refused the token. Sign in again: loomux login")
	case m.notice != "":
		return " " + m.notice
	case m.help:
		return " " + m.hint(keymap.Back, "back") + "  " + m.hint(keymap.Quit, "quit")
	}
	hints := []string{
		m.hint(keymap.Back, "back"),
		m.hint(keymap.Transcript, "transcript"),
		strong.Render(m.keys.Keys(keymap.Up)[0]+"/"+m.keys.Keys(keymap.Down)[0]) + faint.Render(" scroll"),
		m.hint(keymap.Quit, "quit"),
	}
	body := m.body
	if m.tr != nil {
		hints, body = slices.Delete(hints, 1, 2), m.tr.body
	}
	var more string
	if !body.AtBottom() {
		more = "↓ more below"
	}
	return row(" "+strings.Join(hints, "  "), more, m.width)
}

// hint is an action's first key and what it does, as the list's footer
// writes them.
func (m *Model) hint(a keymap.Action, does string) string {
	return strong.Render(m.keys.Keys(a)[0]) + faint.Render(" "+does)
}

func (m *Model) helpLines() []string {
	title, screen := " Keys in a conversation", keymap.ConversationScreen
	if m.tr != nil {
		title, screen = " Keys in a transcript", keymap.TranscriptScreen
	}
	help := strings.TrimRight(m.keys.HelpText(screen), "\n")
	return append([]string{strong.Render(title)}, indent(" ", help)...)
}

// content is the transcript's lines.
func (m *Model) content() []string {
	switch {
	case !m.loaded && m.err != nil:
		return paint(bad, indent(" ", wrap("Couldn't load the conversation: "+errText(m.err), m.width-2)))
	case !m.loaded && m.signedOut:
		return nil
	case !m.loaded:
		return []string{faint.Render(" loading…")}
	}
	var out []string
	switch {
	case m.olderFetching:
		out = append(out, faint.Render(" loading older messages…"), "")
	case m.olderErr != nil:
		out = append(out, paint(bad, indent(" ", wrap("⚠ couldn't load older messages: "+errText(m.olderErr), m.width-2)))...)
		out = append(out, "")
	case m.st.hasMore:
		out = append(out, faint.Render(" ↑ scroll up for older messages"), "")
	}
	items := m.st.timeline()
	if len(items) == 0 {
		out = append(out, faint.Render(" no messages yet"))
	}
	for i, it := range items {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, m.itemLines(it)...)
	}
	return out
}

func (m *Model) itemLines(it item) []string {
	d := it.dispatch
	switch it.kind {
	case itemMessage:
		return m.cached(it.msg.ID, func() []string {
			return m.messageLines(it.msg.Role, it.msg.Content, it.msg.CreatedAt)
		})
	case itemReply:
		var at time.Time
		if d.FinishedAt != nil {
			at = *d.FinishedAt
		}
		return m.cached("reply of "+d.DispatchID, func() []string {
			return m.messageLines(client.RoleAssistant, d.Reply, at)
		})
	case itemInFlight:
		text := "working…"
		if d.Status == client.DispatchQueued {
			text = "queued…"
		}
		since := d.CreatedAt
		if d.StartedAt != nil {
			since = *d.StartedAt
		}
		if c := m.clock(since); c != "" {
			text += " since " + c
		}
		return []string{" " + busy.Render("◐ "+text)}
	}
	text := "✗ the turn failed"
	if d.Status == client.DispatchInterrupted {
		text = "✗ the turn was interrupted"
	}
	if why := line(d.Error); why != "" {
		text += ": " + why
	} else if d.ErrorClass != "" {
		text += " (" + line(d.ErrorClass) + ")"
	}
	return paint(bad, indent(" ", wrap(text, m.width-2)))
}

// cached returns the lines kept under key, rendering them once: a
// message never changes, and markdown is the costly part of a redraw.
func (m *Model) cached(key string, render func() []string) []string {
	if lines, ok := m.cache[key]; ok {
		return lines
	}
	lines := render()
	m.cache[key] = lines
	return lines
}

// messageLines is one message: who and when, then its text. The user's
// is shown as typed, anyone else's as markdown.
func (m *Model) messageLines(role, content string, at time.Time) []string {
	var who string
	switch role {
	case client.RoleUser:
		who = youName.Render("you")
	case client.RoleAssistant:
		who = theirs.Render("loomux")
	default:
		who = theirs.Render(line(role))
	}
	head := " " + who
	if c := m.clock(at); c != "" {
		head += faint.Render(" · " + c)
	}
	width := m.width - len(bodyIndent) - 1
	var body string
	if role == client.RoleUser {
		body = wrap(clean(content), width)
	} else {
		body = m.md.render(content, width)
	}
	if body == "" {
		return []string{head}
	}
	return append([]string{head}, indent(bodyIndent, body)...)
}

// statusLines is one line a task: the open ones, most recently updated
// first, or the latest one when none is open.
func (m *Model) statusLines() []string {
	tasks := slices.Clone(m.st.tasks)
	slices.SortStableFunc(tasks, func(a, b client.Task) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	open := slices.DeleteFunc(slices.Clone(tasks), func(t client.Task) bool {
		return t.Status == client.TaskCompleted || t.Status == client.TaskFailed
	})
	if len(open) == 0 && len(tasks) > 0 {
		open = tasks[:1]
	}
	var lines []string
	for i, t := range open {
		if i == maxStatus-1 && len(open) > maxStatus {
			lines = append(lines, faint.Render(" … and "+plural(len(open)-i, "more open task")))
			break
		}
		lines = append(lines, m.taskLine(t))
	}
	return lines
}

// taskLine is a task's status line: its status, what runs it, why it
// needs the user or failed, and when it last changed.
func (m *Model) taskLine(t client.Task) string {
	glyph, style, label := "·", faint, strings.ReplaceAll(line(t.Status), "_", " ")
	switch t.Status {
	case client.TaskRunning:
		glyph, style = "◐", busy
	case client.TaskAwaitingInput:
		glyph, style = "●", needs
	case client.TaskNeedsAttention:
		glyph, style = "▲", needs
	case client.TaskHumanTakeover:
		glyph, style, label = "⌨", needs, "taken over"
	case client.TaskCompleted:
		glyph, style = "✓", good
	case client.TaskFailed:
		glyph, style = "✗", bad
	}
	left := " " + style.Render(glyph) + " " + label + strings.Repeat(" ", max(len("needs attention")-len(label), 0))
	name, detail := line(t.AgentType), ""
	if name == "" {
		name, detail = line(t.Kind), line(t.Command)
	}
	switch {
	case t.Status == client.TaskNeedsAttention && t.Attention != nil:
		detail = cmp.Or(line(t.Attention.Title), line(t.Attention.Question))
	case t.Status == client.TaskFailed:
		detail = strings.Join(nonEmpty(line(t.ErrorClass), line(t.FailureReason)), ": ")
	}
	for _, part := range nonEmpty(name, detail) {
		left += "  " + part
	}
	when := faint.Render(m.clock(t.UpdatedAt))
	if ansi.StringWidth(left)+2+ansi.StringWidth(when) >= m.width {
		when = "" // the reason matters more than the time
	}
	return row(left, when, m.width)
}

// clock is a time as the transcript tells it: the hour and minute, with
// the day when it isn't today.
func (m *Model) clock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	now := m.now()
	t = t.In(now.Location())
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

// row is one line of width cells with left at its start and right at its
// end, one cell from the edge. left gives way when both don't fit.
func row(left, right string, width int) string {
	rw := ansi.StringWidth(right)
	if rw == 0 {
		return ansi.Truncate(left, width, "…")
	}
	left = ansi.Truncate(left, max(width-rw-3, 0), "…")
	gap := max(width-1-rw-ansi.StringWidth(left), 1)
	return left + strings.Repeat(" ", gap) + right
}

// indent puts prefix before each line of text.
func indent(prefix, text string) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return lines
}

// errText is an error as one line: the server's text for an API error.
func errText(err error) string {
	return line(err.Error())
}

func nonEmpty(parts ...string) []string {
	return slices.DeleteFunc(parts, func(s string) bool { return s == "" })
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

// paint styles each line on its own, so no line is padded to another's
// width.
func paint(style lipgloss.Style, lines []string) []string {
	for i, l := range lines {
		if l != "" {
			lines[i] = style.Render(l)
		}
	}
	return lines
}

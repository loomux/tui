package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/config/keymap"
)

var (
	bold     = lipgloss.NewStyle().Bold(true)
	faint    = lipgloss.NewStyle().Faint(true)
	selected = lipgloss.NewStyle().Reverse(true)
	okStyle  = lipgloss.NewStyle().Foreground(lipgloss.Green)
	errStyle = lipgloss.NewStyle().Foreground(lipgloss.Red)
	glyphs   = map[group]lipgloss.Style{
		needsYou: lipgloss.NewStyle().Foreground(lipgloss.Yellow).Bold(true),
		running:  lipgloss.NewStyle().Foreground(lipgloss.Blue),
		done:     lipgloss.NewStyle().Foreground(lipgloss.Green),
	}
	failedGlyph = lipgloss.NewStyle().Foreground(lipgloss.Red)
)

// columns are a row's widths: status words, workspace, preview, age.
type columns struct{ label, workspace, preview, age int }

const (
	maxLabel     = 24
	maxWorkspace = 16
	minPreview   = 24
	minLabel     = 8
	minWS        = 6
)

// layoutFor sizes the columns to the rows that show, and gives the
// preview the rest of the width: a row is "› ▲ label  workspace
// "preview"  age", width wide (the › marks the cursor's row).
func layoutFor(items []item, width int, now time.Time) columns {
	c := columns{age: 3}
	for _, it := range items {
		c.label = max(c.label, ansi.StringWidth(it.label))
		c.workspace = max(c.workspace, ansi.StringWidth(it.workspace))
		c.age = max(c.age, len(ago(now, it.UpdatedAt)))
	}
	c.label = min(c.label, maxLabel)
	c.workspace = min(c.workspace, maxWorkspace)
	rest := func() int { return width - (4 + c.label + 2 + c.workspace + 2 + 2 + c.age) }
	// Short of room, the preview keeps minPreview columns: a long label
	// gives way first, then the workspace, then both to their minimums.
	for _, s := range []struct {
		col   *int
		floor int
	}{{&c.label, 16}, {&c.workspace, 8}, {&c.label, minLabel}, {&c.workspace, minWS}} {
		for rest() < minPreview && *s.col > s.floor {
			*s.col--
		}
	}
	c.preview = max(1, rest())
	return c
}

// row draws one conversation.
func (c columns) row(it item, now time.Time, isSelected bool) string {
	preview := faint.Render(cell("(no message yet)", c.preview))
	if it.preview != "" {
		preview = cell(`"`+ansi.Truncate(it.preview, c.preview-2, "…")+`"`, c.preview)
	}
	age := ago(now, it.UpdatedAt)
	rest := cell(it.label, c.label) + "  " + faint.Render(cell(it.workspace, c.workspace)) + "  " + preview + "  " +
		faint.Render(strings.Repeat(" ", max(0, c.age-len(age)))+age)
	if isSelected {
		return selected.Render("› " + it.glyph + " " + ansi.Strip(rest))
	}
	style := glyphs[it.group]
	if it.glyph == "✗" {
		style = failedGlyph
	}
	return "  " + style.Render(it.glyph) + " " + rest
}

// cell cuts or pads s to exactly width columns.
func cell(s string, width int) string {
	s = ansi.Truncate(s, width, "…")
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}

// titleRow is a section's title, ruled to the width.
func titleRow(g group, count, width int) string {
	t := fmt.Sprintf(" %s (%d) ", g.title(), count)
	return bold.Render(fit(t+strings.Repeat("─", max(0, width-ansi.StringWidth(t))), width))
}

// ago is a row's age: 42s, 5m, 3h, 2d.
func ago(now, t time.Time) string {
	d := max(0, now.Sub(t))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// header is the first line: the profile on the left; on the right, how
// the last poll went and the help key.
func (a *App) header() string {
	left := " " + bold.Render("loomux") + " · " + a.profile
	var status string
	switch {
	case a.signedOut:
		status = errStyle.Render("⚠ signed out")
	case a.pollErr != nil:
		status = errStyle.Render("⚠ " + clean(a.pollErr.Error()))
	case !a.loaded:
		status = faint.Render("loading…")
	default:
		status = okStyle.Render("●") + " updated " + a.lastOK.Format("15:04:05")
	}
	help := "   " + a.firstKey(keymap.Help) + "=help "
	room := a.width - ansi.StringWidth(left) - ansi.StringWidth(help) - 2
	status = ansi.Truncate(status, max(0, room), "…")
	gap := max(1, a.width-ansi.StringWidth(left)-ansi.StringWidth(status)-ansi.StringWidth(help))
	return fit(left+strings.Repeat(" ", gap)+status+help, a.width)
}

// listBody is the list, or why there isn't one.
func (a *App) listBody() []string {
	height := a.bodyHeight()
	var text []string
	switch {
	case a.signedOut:
		text = []string{"The server refused the token.", clean(a.pollErr.Error())}
	case !a.loaded && a.pollErr != nil:
		text = []string{"Couldn't load the conversations:", clean(a.pollErr.Error())}
	case !a.loaded:
		text = []string{"loading…"}
	default:
		return a.home.render(a.width, height, a.now())
	}
	var out []string
	for _, t := range text {
		out = append(out, fit("   "+t, a.width))
	}
	return pad(out, height)
}

// helpLines is the ? help: the keys in effect on the list and in its
// filter, from the effective keymap.
func (a *App) helpLines() []string {
	var out []string
	section := func(title string, s keymap.Screen) {
		out = append(out, " "+bold.Render(title))
		for _, l := range strings.Split(strings.TrimRight(a.keys.HelpText(s), "\n"), "\n") {
			out = append(out, " "+l)
		}
	}
	section("Keys on the list", keymap.ListScreen)
	out = append(out, "")
	section(fmt.Sprintf("While typing a filter (%s)", a.firstKey(keymap.Filter)), keymap.FilterScreen)
	return out
}

func (a *App) helpBody() []string {
	lines := a.helpLines()
	var out []string
	for _, l := range lines[min(a.helpOffset, len(lines)):] {
		if len(out) == a.bodyHeight() {
			break
		}
		out = append(out, fit(l, a.width))
	}
	return pad(out, a.bodyHeight())
}

// hints are the footer's keys, per screen.
var hints = map[keymap.Screen][]struct {
	action keymap.Action
	label  string
}{
	// The design's mock; the header has the help key, and the help the rest.
	keymap.ListScreen: {
		{keymap.NewConversation, "new"}, {keymap.Open, "open"}, {keymap.Attach, "attach"}, {keymap.Watch, "watch"},
		{keymap.Filter, "filter"}, {keymap.NextGroup, "needs-you/running/done/all"},
	},
	keymap.FilterScreen: {{keymap.Apply, "keep the filter"}, {keymap.Back, "clear it"}},
	keymap.HelpScreen:   {{keymap.Back, "back"}, {keymap.Quit, "quit"}},
}

// footer is the last line: the screen's main keys, or a notice.
func (a *App) footer() string {
	if a.dialog != nil {
		return a.dialogFooter()
	}
	if a.notice != "" {
		return fit(" "+a.notice, a.width)
	}
	var parts []string
	if it, ok := a.home.current(); ok && a.screen == keymap.ListScreen && isHeld(it) {
		if hint := a.takeBackHint(it); hint != "" {
			parts = append(parts, hint)
		}
	}
	for _, h := range hints[a.screen] {
		if k := a.firstKey(h.action); k != "" {
			parts = append(parts, bold.Render(k)+" "+h.label)
		}
	}
	return faint.Render(fit(" "+strings.Join(parts, "  "), a.width))
}

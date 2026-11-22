package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/client"
)

// home is the list's state: the rows, which of them show, the cursor
// and the scroll.
type home struct {
	rows    []client.ConversationSummary // ranked
	hasMore bool
	names   map[string]string
	items   []item // rows, as shown
	visible []item // the items the view and the filter let through
	view    view
	query   string
	cursor  int // into visible
	offset  int // the first body line on screen
}

func (h *home) setRows(rows []client.ConversationSummary, hasMore bool, names map[string]string) {
	h.rows = slices.Clone(rows)
	rank(h.rows)
	h.hasMore = hasMore
	h.names = names
	h.rebuild()
}

func (h *home) setNames(names map[string]string) {
	h.names = names
	h.rebuild()
}

func (h *home) setView(v view) {
	h.view = v
	h.rebuild()
}

func (h *home) setQuery(q string) {
	h.query = q
	h.rebuild()
}

// rebuild remakes the items and keeps the cursor on its conversation if
// it still shows.
func (h *home) rebuild() {
	sel, had := h.current()
	h.items = h.items[:0]
	for _, r := range h.rows {
		h.items = append(h.items, newItem(r, h.names))
	}
	h.visible = h.visible[:0]
	for _, it := range h.items {
		if h.view.shows(it.group) && it.matches(h.query) {
			h.visible = append(h.visible, it)
		}
	}
	if had {
		if i := slices.IndexFunc(h.visible, func(it item) bool { return it.ConversationID == sel.ConversationID }); i >= 0 {
			h.cursor = i
		}
	}
	h.move(0)
}

// selectID puts the cursor on a conversation, showing all of them
// without a filter if the view or the filter hides it. It reports
// whether the list has it.
func (h *home) selectID(id string) bool {
	is := func(it item) bool { return it.ConversationID == id }
	if !slices.ContainsFunc(h.items, is) {
		return false
	}
	if !slices.ContainsFunc(h.visible, is) {
		h.view, h.query = allView, ""
		h.rebuild()
	}
	h.cursor = slices.IndexFunc(h.visible, is)
	return true
}

// current is the item under the cursor.
func (h *home) current() (item, bool) {
	if h.cursor < 0 || h.cursor >= len(h.visible) {
		return item{}, false
	}
	return h.visible[h.cursor], true
}

func (h *home) move(n int) {
	h.cursor = max(0, min(h.cursor+n, len(h.visible)-1))
}

// A line is one line of the list's body.
type line struct {
	kind  lineKind
	group group
	count int    // a title's rows
	item  int    // a row's index in visible
	text  string // a note's text
}

type lineKind int

const (
	titleLine lineKind = iota
	rowLine
	noteLine
)

// lines lays the list out: each group the view shows under its title (in
// the all view, only those with rows), then a note when the server has
// more than the list asked for.
func (h *home) lines() []line {
	var out []line
	query := strings.TrimSpace(h.query)
	noMatch := fmt.Sprintf("no conversation matches %q", clean(query))
	switch {
	case len(h.items) == 0:
		out = append(out, line{kind: noteLine, text: "no conversations yet"})
	case len(h.visible) == 0 && query != "" && h.view == allView:
		out = append(out, line{kind: noteLine, text: noMatch})
	default:
		for _, g := range []group{needsYou, running, done} {
			if !h.view.shows(g) {
				continue
			}
			var rows []line
			for i, it := range h.visible {
				if it.group == g {
					rows = append(rows, line{kind: rowLine, item: i})
				}
			}
			if len(rows) == 0 && h.view == allView {
				continue
			}
			out = append(out, line{kind: titleLine, group: g, count: len(rows)})
			switch {
			case len(rows) > 0:
			case query != "":
				out = append(out, line{kind: noteLine, text: noMatch})
			default:
				out = append(out, line{kind: noteLine, text: emptyGroup[g]})
			}
			out = append(out, rows...)
		}
	}
	if h.hasMore {
		out = append(out, line{kind: noteLine, text: fmt.Sprintf("only the newest %d conversations are shown", pollLimit)})
	}
	return out
}

var emptyGroup = map[group]string{
	needsYou: "nothing needs you",
	running:  "nothing is running",
	done:     "nothing has finished yet",
}

// scroll keeps the cursor's row on screen, with its section's title when
// it's the section's first row, and on the last row, the notes after it.
func (h *home) scroll(height int) {
	lines := h.lines()
	at := slices.IndexFunc(lines, func(l line) bool { return l.kind == rowLine && l.item == h.cursor })
	if at >= 0 {
		top, bottom := at, at
		if top > 0 && lines[top-1].kind == titleLine {
			top--
		}
		if h.cursor == len(h.visible)-1 {
			bottom = len(lines) - 1
		}
		if top < h.offset {
			h.offset = top
		}
		if bottom >= h.offset+height {
			h.offset = min(bottom-height+1, at) // never past the cursor
		}
	}
	h.offset = max(0, min(h.offset, len(lines)-height))
}

// render draws the body: height lines, width wide.
func (h *home) render(width, height int, now time.Time) []string {
	lines := h.lines()
	cols := layoutFor(h.visible, width, now)
	var out []string
	for _, l := range lines[min(h.offset, len(lines)):] {
		if len(out) == height {
			break
		}
		switch l.kind {
		case titleLine:
			out = append(out, titleRow(l.group, l.count, width))
		case rowLine:
			out = append(out, cols.row(h.visible[l.item], now, l.item == h.cursor))
		default:
			out = append(out, fit("   "+l.text, width))
		}
	}
	return pad(out, height)
}

// pad fills lines up to height with empty ones.
func pad(lines []string, height int) []string {
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

// fit cuts a line to width, with "…" where it was cut.
func fit(s string, width int) string {
	return ansi.Truncate(s, width, "…")
}

package conversation

import (
	"regexp"
	"strings"
	"unicode"

	"charm.land/glamour/v2"
	gansi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

// clean returns s as text that is safe to draw. What the server sends (a
// message, a prompt, a pane) is shown, never obeyed: escape sequences,
// control characters and bidi controls are dropped, so a reply can't
// retitle the terminal, move the cursor, hide what follows or reorder
// what a line shows. A tab becomes four spaces and a line ends with
// "\n" alone.
func clean(s string) string {
	s = ansi.Strip(s)
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			sb.WriteRune(r)
		case r == '\t':
			sb.WriteString("    ")
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
			// C0, DEL, C1 and the bidi overrides: dropped.
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// wrap breaks plain text into lines of at most width cells, at spaces
// where it can and inside a word that is longer than a line.
func wrap(s string, width int) string {
	lines := strings.Split(ansi.Wrap(s, max(width, 1), ""), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

// minMarkdownWidth is the narrowest markdown is laid out at.
const minMarkdownWidth = 10

// markdown renders messages as markdown (Glamour), for a dark or a light
// background. It keeps one renderer for the width in use.
type markdown struct {
	dark  bool
	width int
	r     *glamour.TermRenderer
}

func newMarkdown() *markdown { return &markdown{dark: true} }

// setDark chooses the styling for a dark or a light background.
func (md *markdown) setDark(dark bool) {
	if md.dark != dark {
		md.dark, md.r = dark, nil
	}
}

// padding is what Glamour fills a line up to the width with: spaces,
// each in its own styling.
var padding = regexp.MustCompile(`(?:\x1b\[[0-9;]*m *\x1b\[0?m| )+$`)

// render lays text out as markdown, at most width cells wide: no blank
// lines around it, no padding after a line, and a line Glamour leaves
// too wide (a heading of one long word) continued on the next rather
// than cut. Text that can't be rendered is shown as it is.
func (md *markdown) render(text string, width int) string {
	width = max(width, minMarkdownWidth)
	text = clean(text)
	if md.r == nil || md.width != width {
		r, err := glamour.NewTermRenderer(glamour.WithStyles(md.style()), glamour.WithWordWrap(width))
		if err != nil {
			return wrap(text, width)
		}
		md.r, md.width = r, width
	}
	out, err := md.r.Render(text)
	if err != nil {
		return wrap(text, width)
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = padding.ReplaceAllString(l, "")
		if ansi.StringWidth(l) > width {
			lines = append(lines, strings.Split(ansi.Hardwrap(l, width, true), "\n")...)
			continue
		}
		lines = append(lines, l)
	}
	blank := func(l string) bool { return ansi.StringWidth(l) == 0 }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// style is Glamour's standard style for the background, without the
// margin and the blank lines it puts around a document.
func (md *markdown) style() gansi.StyleConfig {
	cfg := styles.DarkStyleConfig
	if !md.dark {
		cfg = styles.LightStyleConfig
	}
	var none uint
	cfg.Document.Margin = &none
	cfg.Document.BlockPrefix, cfg.Document.BlockSuffix = "", ""
	return cfg
}

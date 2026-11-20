package conversation

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// plain is what a rendered text shows: its lines without their styling.
func plain(s string) []string {
	return strings.Split(ansi.Strip(s), "\n")
}

func checkWidth(t *testing.T, rendered string, width int) {
	t.Helper()
	for _, line := range strings.Split(rendered, "\n") {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("a line is %d wide, over %d: %q", w, width, ansi.Strip(line))
		}
	}
}

// What the server sends is shown, never obeyed: an agent's reply or a
// pane may hold escape sequences that would retitle the terminal, move
// the cursor or hide text.
func TestCleanLeavesOnlyText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                             "plain",
		"line\nnext":                        "line\nnext",
		"ok\x1b[31mred\x1b[0m":              "okred",
		"a\x1b]0;evil title\x07b":           "ab",
		"a\x1b]8;;https://example.net\x07b": "ab",
		"clear\x1b[2J\x1b[Hhome":            "clearhome",
		"bell\x07 nul\x00 bs\x08":           "bell nul bs",
		"dos\r\nline":                       "dos\nline",
		"over\rwritten":                     "overwritten",
		"a\tb":                              "a    b",
		"c1 \u009b31mcsi":                   "c1 31mcsi",
		"esc alone \x1b":                    "esc alone ",
		"héllo → ✓ 日本":                      "héllo → ✓ 日本",
		// A right-to-left override would show the rest of the line
		// backwards: "safe.exe" as "exe.efas".
		"run \u202eexe.efas\u202c now": "run exe.efas now",
		"\u2066isolated\u2069":         "isolated",
	} {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMarkdownIsRenderedNotShownAsSource(t *testing.T) {
	md := newMarkdown()
	out := md.render("Fixed **two** things:\n\n- the `retry` loop\n- the timeout\n\n## Next\n\nRun it.", 60)

	want := []string{
		"Fixed two things:",
		"",
		"• the retry loop",
		"• the timeout",
		"",
		"## Next",
		"",
		"Run it.",
	}
	got := plain(out)
	for i := range got {
		got[i] = strings.ReplaceAll(got[i], " ", "") // the padding of inline code
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rendered text:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("the rendering carries no styling at all")
	}
}

func TestMarkdownWrapsToTheWidth(t *testing.T) {
	md := newMarkdown()
	text := "The quick brown fox jumps over the lazy dog, again and again, until the line is long enough to wrap twice."
	out := md.render(text, 30)

	checkWidth(t, out, 30)
	if got := strings.Join(strings.Fields(strings.Join(plain(out), " ")), " "); got != text {
		t.Errorf("wrapped text = %q, want every word of %q", got, text)
	}
	if len(plain(out)) < 4 {
		t.Errorf("%d lines at width 30, want the text wrapped", len(plain(out)))
	}
}

// Glamour pads each line to the width and frames the text in blank
// lines: neither belongs in a transcript, or in what is copied from it.
func TestMarkdownHasNoPaddingAroundIt(t *testing.T) {
	md := newMarkdown()
	lines := plain(md.render("one\n\ntwo", 40))

	if want := []string{"one", "", "two"}; strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", lines, want)
	}
}

// Nothing is cut at the width, not even where Glamour doesn't wrap: a
// heading that is one long word continues on the next line.
func TestMarkdownKeepsEveryCharacterOfATextThatCannotWrap(t *testing.T) {
	md := newMarkdown()
	word := strings.Repeat("A1b2C3d4", 12)
	for name, text := range map[string]string{
		"a heading":   "# " + word,
		"a paragraph": word,
		"a code line": "```\n" + word + "\n```",
		"a list item": "- " + word,
		"a table":     "| a | b |\n|---|---|\n| " + word + " | x |",
	} {
		out := md.render(text, 40)
		checkWidth(t, out, 40)
		var shown strings.Builder
		for _, l := range plain(out) {
			if i := strings.Index(l, "A1b2"); i >= 0 || strings.Contains(word, strings.Trim(l, " │•#x")) {
				shown.WriteString(strings.Trim(l, " │•#x"))
			}
		}
		if !strings.Contains(shown.String(), word) {
			t.Errorf("%s: the word as shown is %q, want all of %q", name, shown.String(), word)
		}
	}
}

// A code line is never cut at the width: what doesn't fit continues on
// the next line.
func TestMarkdownKeepsEveryCharacterOfALongCodeLine(t *testing.T) {
	md := newMarkdown()
	code := "kubectl --context example rollout restart deployment/api --namespace demo --timeout=120s"
	out := md.render("```\n"+code+"\n  indented\n```", 40)

	checkWidth(t, out, 40)
	joined := strings.ReplaceAll(strings.Join(plain(out), ""), " ", "")
	if want := strings.ReplaceAll(code, " ", ""); !strings.Contains(joined, want) {
		t.Errorf("the code line lost characters:\n%q\nwant it to hold\n%q", joined, want)
	}
	if !strings.Contains(strings.Join(plain(out), "\n"), "  indented") {
		t.Errorf("the code's indentation is gone:\n%s", strings.Join(plain(out), "\n"))
	}
}

func TestMarkdownNeverPassesEscapeSequencesThrough(t *testing.T) {
	md := newMarkdown()
	out := md.render("before \x1b]0;evil title\x07 `code \x1b[2J` after\n\n```\n\x1b[H\x1b[31mpane\n```", 60)

	for _, bad := range []string{"\x1b]0;", "\x1b[2J", "\x1b[H", "\x07evil"} {
		if strings.Contains(out, bad) {
			t.Errorf("the rendering holds %q from the message", bad)
		}
	}
	if text := strings.Join(plain(out), "\n"); !strings.Contains(text, "before") || !strings.Contains(text, "after") || !strings.Contains(text, "pane") {
		t.Errorf("the text around the sequences is gone:\n%s", text)
	}
}

func TestMarkdownStyleFollowsTheBackground(t *testing.T) {
	md := newMarkdown()
	dark := md.render("plain text", 40)
	md.setDark(false)
	light := md.render("plain text", 40)

	if dark == light {
		t.Error("the same styling on a dark and on a light background")
	}
	if strings.Join(plain(dark), "\n") != strings.Join(plain(light), "\n") {
		t.Errorf("the text differs: %q and %q", plain(dark), plain(light))
	}
}

// One renderer lays out every message of the transcript.
func TestMarkdownRendererIsReused(t *testing.T) {
	md := newMarkdown()
	first := md.render("# One\n\n- a\n- b", 40)
	md.render("| x | y |\n|---|---|\n| 1 | 2 |", 40)
	if again := md.render("# One\n\n- a\n- b", 40); again != first {
		t.Errorf("the same text rendered differently the second time:\n%q\n%q", first, again)
	}
}

func TestWrapKeepsTextWithinTheWidth(t *testing.T) {
	out := wrap("a first line that is long enough to wrap\n\n  indented\nsupercalifragilisticexpialidocious-and-then-some", 20)

	checkWidth(t, out, 20)
	want := []string{
		"a first line that is",
		"long enough to wrap",
		"",
		"  indented",
		"supercalifragilistic",
		"expialidocious-and-", // a hyphen is a place to break
		"then-some",
	}
	if got := strings.Split(out, "\n"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrap =\n%q\nwant\n%q", got, want)
	}
}

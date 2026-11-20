package attach

import (
	"slices"
	"strings"
	"testing"
)

func TestSplitWords(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"tmux -L loomux attach -t loomux-abc", []string{"tmux", "-L", "loomux", "attach", "-t", "loomux-abc"}},
		{"ssh -p 2222 loomux@box.example.net -t 'tmux -L loomux attach -t s'",
			[]string{"ssh", "-p", "2222", "loomux@box.example.net", "-t", "tmux -L loomux attach -t s"}},
		{`kubectl -n ns exec -it pod -- tmux -L "my sock" attach -t s`,
			[]string{"kubectl", "-n", "ns", "exec", "-it", "pod", "--", "tmux", "-L", "my sock", "attach", "-t", "s"}},
		{"  a \t b  ", []string{"a", "b"}},
		{`a 'it'\''s' b`, []string{"a", "it's", "b"}},
		{`"a \"b\" \\ c"`, []string{`a "b" \ c`}},
		{`"a\b"`, []string{`a\b`}}, // a backslash before another character stays, as in sh
		{`a\ b c`, []string{"a b", "c"}},
		{`a '' b`, []string{"a", "", "b"}},
		{`a ""`, []string{"a", ""}},
		{`a'b'"c"d`, []string{"abcd"}},
		{"a#b c=d --context=x b~c", []string{"a#b", "c=d", "--context=x", "b~c"}},
		{`a '$HOME' '*' ';' '|' "~" "#"`, []string{"a", "$HOME", "*", ";", "|", "~", "#"}},
		{`a \$x \* \;`, []string{"a", "$x", "*", ";"}},
		{"docker -H ssh://box.example.net exec -it c tmux attach", []string{"docker", "-H", "ssh://box.example.net", "exec", "-it", "c", "tmux", "attach"}},
	} {
		got, err := splitWords(tc.line)
		if err != nil {
			t.Errorf("%q: %v", tc.line, err)
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%q = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// A line that means more to a shell than its words is refused, never run
// as something else: the client has no shell to give it to.
func TestSplitWordsRefusesWhatNeedsAShell(t *testing.T) {
	for _, line := range []string{
		"",
		"   ",
		"a; rm -rf x",
		"a | b",
		"a && b",
		"a & b",
		"a $(b)",
		"a `b`",
		"a $HOME",
		`a "$HOME"`,
		"a \"`b`\"",
		"a > f",
		"a < f",
		"a (b)",
		"a *",
		"a b?",
		"a [bc]",
		"a {b,c}",
		"a ~/x",
		"~/bin/a b",
		"a # comment",
		"X=1 a",
		"a\nb",
		"a 'b",
		`a "b`,
		`a b\`,
		"a\x00b",
		"a \x1b[2J",
	} {
		got, err := splitWords(line)
		if err == nil {
			t.Errorf("%q = %q, want an error", line, got)
			continue
		}
		// The error says what is wrong without repeating a control
		// character to the terminal.
		if strings.ContainsAny(err.Error(), "\x00\x1b\n") {
			t.Errorf("%q: the error %q has a control character", line, err)
		}
	}
}

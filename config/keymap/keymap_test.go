package keymap

import (
	"slices"
	"strings"
	"testing"
)

// The design's keybindings (decided 1) are the defaults.
func TestDefaults(t *testing.T) {
	m := Default()
	want := map[Action][]string{
		Up:         {"k", "up"},
		Down:       {"j", "down"},
		Top:        {"g", "home"},
		Bottom:     {"G", "end"},
		Filter:     {"/"},
		Open:       {"enter"},
		Back:       {"esc"},
		New:        {"n"},
		Attach:     {"a"},
		Watch:      {"w"},
		Yes:        {"y"},
		No:         {"n"},
		Cancel:     {"x", "ctrl+c"},
		Browser:    {"o"},
		Transcript: {"p"},
		Help:       {"?"},
		Send:       {"enter"},
		Newline:    {"alt+enter"},
		Choose:     {"1", "2", "3", "4", "5", "6", "7", "8", "9"},
		Reply:      {"r"},
		NextGroup:  {"tab"},
	}
	for a, keys := range want {
		if got := m.Keys(a); !slices.Equal(got, keys) {
			t.Errorf("%s: keys %q, want %q", a, got, keys)
		}
	}
	for _, a := range Actions() {
		if len(m.Keys(a)) == 0 {
			t.Errorf("%s has no default key", a)
		}
	}
}

// The defaults are valid: New with no overrides is Default, without an
// error (in particular, no two actions share a key on one screen).
func TestDefaultsValid(t *testing.T) {
	m, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range Actions() {
		if !slices.Equal(m.Keys(a), Default().Keys(a)) {
			t.Errorf("%s: New(nil) %q, Default %q", a, m.Keys(a), Default().Keys(a))
		}
	}
}

func TestLookup(t *testing.T) {
	m := Default()
	cases := []struct {
		screen Screen
		key    string
		want   Action
		index  int
		ok     bool
	}{
		{ListScreen, "n", New, 0, true},
		{ConversationScreen, "n", No, 0, true}, // the same key, another screen
		{ListScreen, "j", Down, 0, true},
		{ListScreen, "down", Down, 1, true},
		{ListScreen, "G", Bottom, 0, true},
		{ListScreen, "g", Top, 0, true},
		{ListScreen, "enter", Open, 0, true},
		{ComposerScreen, "enter", Send, 0, true},
		{ComposerScreen, "alt+enter", Newline, 0, true},
		{ConversationScreen, "3", Choose, 2, true}, // option 3
		{ConversationScreen, "ctrl+c", Cancel, 1, true},
		{ComposerScreen, "ctrl+c", Cancel, 1, true},
		// In the composer a key that types text types it.
		{ComposerScreen, "x", "", 0, false},
		{ComposerScreen, "n", "", 0, false},
		{ComposerScreen, "?", "", 0, false},
		{ListScreen, "p", "", 0, false}, // transcript is a conversation action
		{TranscriptScreen, "q", Quit, 0, true},
		{HelpScreen, "?", Help, 0, true},
		{ListScreen, "nope", "", 0, false},
		// Bubble Tea v1 reports the space bar as " ".
		{ListScreen, " ", "", 0, false},
	}
	for _, c := range cases {
		a, i, ok := m.Lookup(c.screen, c.key)
		if a != c.want || i != c.index || ok != c.ok {
			t.Errorf("Lookup(%s, %q) = %q, %d, %v; want %q, %d, %v", c.screen, c.key, a, i, ok, c.want, c.index, c.ok)
		}
	}
}

func TestOverride(t *testing.T) {
	m, err := New(map[string][]string{
		"attach": {"A"},
		"send":   {"ctrl+s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Keys(Attach); !slices.Equal(got, []string{"A"}) {
		t.Errorf("attach = %q", got)
	}
	// An override replaces the defaults: "a" no longer attaches.
	if a, _, ok := m.Lookup(ListScreen, "a"); ok {
		t.Errorf(`"a" on the list is still %s`, a)
	}
	if a, _, ok := m.Lookup(ConversationScreen, "A"); !ok || a != Attach {
		t.Errorf(`"A" in a conversation = %q, %v`, a, ok)
	}
	if a, _, ok := m.Lookup(ComposerScreen, "ctrl+s"); !ok || a != Send {
		t.Errorf(`"ctrl+s" in the composer = %q, %v`, a, ok)
	}
	if _, _, ok := m.Lookup(ComposerScreen, "enter"); ok {
		t.Error(`"enter" still sends`)
	}
	// Overrides don't change the defaults.
	if got := Default().Keys(Attach); !slices.Equal(got, []string{"a"}) {
		t.Errorf("Default attach = %q after an override", got)
	}
}

// Swapping two keys is fine: the clash check runs on the effective map.
func TestOverrideSwap(t *testing.T) {
	m, err := New(map[string][]string{"new": {"a"}, "attach": {"n"}})
	if err != nil {
		t.Fatal(err)
	}
	if a, _, _ := m.Lookup(ListScreen, "a"); a != New {
		t.Errorf(`"a" = %q, want new`, a)
	}
	if a, _, _ := m.Lookup(ListScreen, "n"); a != Attach {
		t.Errorf(`"n" = %q, want attach`, a)
	}
}

// Overriding choose sets the keys for options 1, 2, … in order.
func TestOverrideChoose(t *testing.T) {
	m, err := New(map[string][]string{"choose": {"&", "é", "\""}})
	if err != nil {
		t.Fatal(err)
	}
	if a, i, ok := m.Lookup(ConversationScreen, "é"); !ok || a != Choose || i != 1 {
		t.Errorf(`"é" = %q, %d, %v; want choose, 1 (option 2)`, a, i, ok)
	}
	if _, _, ok := m.Lookup(ConversationScreen, "1"); ok {
		t.Error(`"1" still chooses`)
	}
}

func TestNewErrors(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string][]string
		want      []string // substrings of the error
	}{
		{"unknown action", map[string][]string{"atach": {"A"}},
			[]string{`keys.atach: unknown action`, "attach"}},
		{"clash on one screen", map[string][]string{"new": {"a"}},
			[]string{`"a"`, "attach", "new", "list"}},
		{"clash with a global action", map[string][]string{"transcript": {"q"}},
			[]string{`"q"`, "quit", "transcript", "conversation"}},
		{"clash within the composer", map[string][]string{"newline": {"enter"}},
			[]string{`"enter"`, "newline", "send", "composer"}},
		{"bad key", map[string][]string{"attach": {"ctrl+"}},
			[]string{"keys.attach", `"ctrl+"`}},
		{"no key", map[string][]string{"attach": {}},
			[]string{"keys.attach", "no key"}},
		{"empty key", map[string][]string{"attach": {""}},
			[]string{"keys.attach", "no key"}},
		{"composer action typing text", map[string][]string{"send": {"s"}},
			[]string{"keys.send", `"s"`, "types text"}},
		{"shift with a character", map[string][]string{"attach": {"shift+a"}},
			[]string{"keys.attach", `"A"`}},
		{"several problems", map[string][]string{"atach": {"A"}, "watch": {"ctrl+"}},
			[]string{"keys.atach", "keys.watch"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := New(c.overrides)
			if err == nil {
				t.Fatal("no error")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q doesn't mention %s", err, w)
				}
			}
		})
	}
}

// Keys are written as Bubble Tea names them (ctrl+s, alt+enter, esc,
// pgup), and read leniently: the modifiers in any order, "-" or "+",
// and the names in any case.
func TestCanonical(t *testing.T) {
	ok := map[string]string{
		"a":           "a",
		"A":           "A",
		"?":           "?",
		"-":           "-",
		"+":           "+",
		"é":           "é",
		" ":           "space",
		"space":       "space",
		"Space":       "space",
		"enter":       "enter",
		"Return":      "enter",
		"escape":      "esc",
		"ESC":         "esc",
		"ctrl+s":      "ctrl+s",
		"ctrl-s":      "ctrl+s",
		"Ctrl+S":      "ctrl+s",
		"ctrl++":      "ctrl++",
		"ctrl+-":      "ctrl+-",
		"alt+enter":   "alt+enter",
		"alt-enter":   "alt+enter",
		"alt+ctrl+x":  "ctrl+alt+x",
		"alt+A":       "alt+A",
		"shift+tab":   "shift+tab",
		"shift+up":    "shift+up",
		"pageup":      "pgup",
		"pgdown":      "pgdown",
		"del":         "delete",
		"f5":          "f5",
		"F12":         "f12",
		"ctrl+alt+up": "ctrl+alt+up",
	}
	for in, want := range ok {
		got, err := Canonical(in)
		if err != nil || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ctrl+", "ctrl", "foo", "ab", "ctrl+ctrl+a", "shift+a", "shift+1", " a", "a ", "f0", "f21", "\t", "\x00", "hyper+a"} {
		if got, err := Canonical(bad); err == nil {
			t.Errorf("Canonical(%q) = %q, want an error", bad, got)
		}
	}
}

// Two spellings of one key are one key.
func TestOverrideDedup(t *testing.T) {
	m, err := New(map[string][]string{"cancel": {"ctrl+c", "Ctrl-C", "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Keys(Cancel); !slices.Equal(got, []string{"ctrl+c", "x"}) {
		t.Errorf("cancel = %q", got)
	}
}

// Every action has a help line, and the help for a screen lists the
// actions active on it, in the keymap's order, with their effective keys.
func TestHelp(t *testing.T) {
	m, err := New(map[string][]string{"attach": {"A"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range Screens() {
		for _, b := range m.Help(s) {
			if b.Help == "" {
				t.Errorf("%s: %s has no help", s, b.Action)
			}
		}
	}
	list := m.Help(ListScreen)
	var names []Action
	for _, b := range list {
		names = append(names, b.Action)
		if b.Action == Attach && !slices.Equal(b.Keys, []string{"A"}) {
			t.Errorf("help shows attach as %q, want the override", b.Keys)
		}
	}
	for _, a := range []Action{Up, Down, Open, Back, New, Attach, Watch, Filter, NextGroup, Browser, Help, Quit} {
		if !slices.Contains(names, a) {
			t.Errorf("list help lacks %s: %v", a, names)
		}
	}
	for _, a := range []Action{Send, Yes, No, Choose, Transcript} {
		if slices.Contains(names, a) {
			t.Errorf("list help has %s", a)
		}
	}
	text := m.HelpText(ConversationScreen)
	for _, w := range []string{"1-9", "k/up", "A ", "x/ctrl+c"} {
		if !strings.Contains(text, w) {
			t.Errorf("conversation help lacks %q:\n%s", w, text)
		}
	}
}

// The composer's help shows only the keys that work there: a key that
// types text isn't one.
func TestHelpComposer(t *testing.T) {
	text := Default().HelpText(ComposerScreen)
	if !strings.Contains(text, "ctrl+c") || strings.Contains(text, "x/") {
		t.Errorf("composer help:\n%s", text)
	}
	for _, w := range []string{"enter", "alt+enter", "esc"} {
		if !strings.Contains(text, w) {
			t.Errorf("composer help lacks %q:\n%s", w, text)
		}
	}
}

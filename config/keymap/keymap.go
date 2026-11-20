// Package keymap is the client's one keymap (the design in loomux/server,
// docs/design/tui-client-design.md, "Config", decided 1): every binding is
// an action name with vim-style default keys, the config's [keys] table
// overrides any of them, and the ? help is built from the effective map.
//
//	[keys]
//	attach = "A"
//	send = "ctrl+s"
//	cancel = ["x", "ctrl+c"]
//
// An override replaces the action's default keys. An unknown action, a
// key that isn't one, or two actions with the same key on one screen is
// an error at start (New), never silently ignored.
//
// Keys are written as Bubble Tea names them (KeyMsg.String()): a
// character ("a", "A", "?"), a named key (enter, esc, tab, space,
// backspace, delete, insert, up, down, left, right, home, end, pgup,
// pgdown, f1-f20) and modifiers before it (ctrl+, alt+, shift+ with a
// named key). Canonical spells each key one way, so a key read from the
// config and a key the terminal sends compare equal.
package keymap

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A Screen is where keys are read. Each action is active on some screens,
// and no two actions active on one screen may share a key.
type Screen string

const (
	ListScreen         Screen = "list"         // home: the attention-first list
	ConversationScreen Screen = "conversation" // a conversation, its cards and tasks
	ComposerScreen     Screen = "composer"     // writing a message: keys that type text type it
	TranscriptScreen   Screen = "transcript"   // a task's transcript
	HelpScreen         Screen = "help"         // the ? help
)

// Screens returns every screen, in a fixed order.
func Screens() []Screen {
	return []Screen{ListScreen, ConversationScreen, ComposerScreen, TranscriptScreen, HelpScreen}
}

// An Action is what a key does, named as [keys] names it.
type Action string

const (
	Up              Action = "up"
	Down            Action = "down"
	PageUp          Action = "page_up"
	PageDown        Action = "page_down"
	Top             Action = "top"
	Bottom          Action = "bottom"
	Open            Action = "open"
	Back            Action = "back"
	Filter          Action = "filter"
	NextGroup       Action = "next_group"
	NewConversation Action = "new"
	Attach          Action = "attach"
	Watch           Action = "watch"
	Browser         Action = "browser"
	Transcript      Action = "transcript"
	Cancel          Action = "cancel"
	Yes             Action = "yes"
	No              Action = "no"
	Choose          Action = "choose" // the key at index i chooses option i+1
	Reply           Action = "reply"
	Send            Action = "send"
	Newline         Action = "newline"
	Help            Action = "help"
	Quit            Action = "quit"
)

var (
	browse   = []Screen{ListScreen, ConversationScreen, TranscriptScreen, HelpScreen}
	onTask   = []Screen{ListScreen, ConversationScreen}
	backable = []Screen{ListScreen, ConversationScreen, ComposerScreen, TranscriptScreen, HelpScreen}
)

type def struct {
	action  Action
	keys    []string // the defaults, canonical
	screens []Screen
	help    string
}

// defs is the keymap: its order is the help's.
var defs = []def{
	{Up, []string{"k", "up"}, browse, "move up"},
	{Down, []string{"j", "down"}, browse, "move down"},
	{PageUp, []string{"pgup", "ctrl+u"}, browse, "page up"},
	{PageDown, []string{"pgdown", "ctrl+d"}, browse, "page down"},
	{Top, []string{"g", "home"}, browse, "go to the top"},
	{Bottom, []string{"G", "end"}, browse, "go to the bottom"},
	{Open, []string{"enter"}, []Screen{ListScreen}, "open the conversation"},
	{Back, []string{"esc"}, backable, "back"},
	{Filter, []string{"/"}, []Screen{ListScreen}, "filter"},
	{NextGroup, []string{"tab"}, []Screen{ListScreen}, "show needs you, running, done or all"},
	{NewConversation, []string{"n"}, []Screen{ListScreen}, "new conversation"},
	{Attach, []string{"a"}, onTask, "attach to the agent"},
	{Watch, []string{"w"}, onTask, "watch the agent (read-only)"},
	{Browser, []string{"o"}, onTask, "open in the browser"},
	{Transcript, []string{"p"}, []Screen{ConversationScreen}, "show the task's transcript"},
	{Cancel, []string{"x", "ctrl+c"}, []Screen{ConversationScreen, ComposerScreen}, "cancel the turn in flight"},
	{Yes, []string{"y"}, []Screen{ConversationScreen}, "answer yes"},
	{No, []string{"n"}, []Screen{ConversationScreen}, "answer no"},
	{Choose, []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}, []Screen{ConversationScreen}, "choose an option by its number"},
	{Reply, []string{"r"}, []Screen{ConversationScreen}, "reply with text"},
	{Send, []string{"enter"}, []Screen{ComposerScreen}, "send"},
	{Newline, []string{"alt+enter"}, []Screen{ComposerScreen}, "new line"},
	{Help, []string{"?"}, browse, "help"},
	{Quit, []string{"q"}, browse, "quit"},
}

// Actions returns every action, in the keymap's order.
func Actions() []Action {
	out := make([]Action, len(defs))
	for i, d := range defs {
		out[i] = d.action
	}
	return out
}

// Map is an effective keymap: the defaults with the overrides applied.
type Map struct {
	keys     map[Action][]string
	byScreen map[Screen]map[string]Action
}

// Default returns the default keymap.
func Default() *Map {
	m, err := New(nil)
	if err != nil {
		panic("keymap: the defaults are invalid: " + err.Error())
	}
	return m
}

// New returns the defaults with overrides applied: action name → keys,
// as the config's [keys] table gives them. Every problem is reported in
// one error.
func New(overrides map[string][]string) (*Map, error) {
	var problems []string
	m := &Map{keys: map[Action][]string{}, byScreen: map[Screen]map[string]Action{}}
	overridden := map[Action]bool{}
	for _, d := range defs {
		m.keys[d.action] = d.keys
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		a := Action(name)
		d, ok := lookupDef(a)
		if !ok {
			// The name only: a value under an unknown name isn't repeated.
			problems = append(problems, fmt.Sprintf("keys.%s: unknown action; the actions are %s", name, actionList()))
			continue
		}
		keys, err := canonicalKeys(overrides[name])
		if err != nil {
			problems = append(problems, fmt.Sprintf("keys.%s: %v", name, err))
			continue
		}
		if onlyOn(d, ComposerScreen) && !slices.ContainsFunc(keys, func(k string) bool { return !typesText(k) }) {
			problems = append(problems, fmt.Sprintf("keys.%s: %s types text in the composer; give a ctrl+ or alt+ key or a named key such as enter", name, quoted(keys)))
			continue
		}
		m.keys[a] = keys
		overridden[a] = true
	}
	for _, s := range Screens() {
		bound := map[string]Action{}
		for _, d := range defs {
			if !slices.Contains(d.screens, s) {
				continue
			}
			for _, k := range m.keys[d.action] {
				if s == ComposerScreen && typesText(k) {
					continue // it types the character
				}
				if prev, ok := bound[k]; ok {
					problems = append(problems, clash(s, k, prev, d.action, overridden))
					continue
				}
				bound[k] = d.action
			}
		}
		m.byScreen[s] = bound
	}
	switch len(problems) {
	case 0:
		return m, nil
	case 1:
		return nil, errors.New(problems[0])
	}
	return nil, fmt.Errorf("%d problems in [keys]:\n  %s", len(problems), strings.Join(problems, "\n  "))
}

func clash(s Screen, key string, a, b Action, overridden map[Action]bool) string {
	var named []string
	for _, x := range []Action{a, b} {
		if overridden[x] {
			named = append(named, "keys."+string(x))
		}
	}
	msg := fmt.Sprintf("keys: %q is both %s and %s on the %s screen", key, a, b, s)
	if len(named) > 0 {
		msg += " (set by " + strings.Join(named, " and ") + "): give one of them another key"
	}
	return msg
}

func lookupDef(a Action) (def, bool) {
	for _, d := range defs {
		if d.action == a {
			return d, true
		}
	}
	return def{}, false
}

func onlyOn(d def, s Screen) bool {
	return len(d.screens) == 1 && d.screens[0] == s
}

func actionList() string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = string(d.action)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func canonicalKeys(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, errors.New("no key: give a key or a list of keys")
	}
	var out []string
	for _, k := range in {
		c, err := Canonical(k)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

func quoted(keys []string) string {
	q := make([]string, len(keys))
	for i, k := range keys {
		q[i] = strconv.Quote(k)
	}
	return strings.Join(q, ", ")
}

// Keys returns the action's effective keys, canonical, in order.
func (m *Map) Keys(a Action) []string {
	return slices.Clone(m.keys[a])
}

// Lookup returns the action a key does on a screen, and the key's index
// among the action's keys (for Choose, index i chooses option i+1). key
// is a Bubble Tea KeyMsg.String(), or any spelling Canonical reads. In the
// composer a key that types text never names an action.
func (m *Map) Lookup(s Screen, key string) (Action, int, bool) {
	k, err := Canonical(key)
	if err != nil {
		return "", 0, false
	}
	a, ok := m.byScreen[s][k]
	if !ok {
		return "", 0, false
	}
	return a, slices.Index(m.keys[a], k), true
}

// A Binding is one line of help: an action and its effective keys.
type Binding struct {
	Action Action
	Keys   []string
	Help   string
}

// Help returns the bindings active on a screen, in the keymap's order,
// with the keys that work there.
func (m *Map) Help(s Screen) []Binding {
	var out []Binding
	for _, d := range defs {
		if !slices.Contains(d.screens, s) {
			continue
		}
		var keys []string
		for _, k := range m.keys[d.action] {
			if m.byScreen[s][k] == d.action {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			out = append(out, Binding{Action: d.action, Keys: keys, Help: d.help})
		}
	}
	return out
}

// HelpText renders Help(s) as plain text, one binding a line: the keys
// ("k/up", a run of characters as "1-9"), then what they do.
func (m *Map) HelpText(s Screen) string {
	bs := m.Help(s)
	cols := make([]string, len(bs))
	width := 0
	for i, b := range bs {
		cols[i] = KeysText(b.Keys)
		width = max(width, utf8.RuneCountInString(cols[i]))
	}
	var sb strings.Builder
	for i, b := range bs {
		pad := width - utf8.RuneCountInString(cols[i])
		fmt.Fprintf(&sb, "  %s%s  %s\n", cols[i], strings.Repeat(" ", pad), b.Help)
	}
	return sb.String()
}

// KeysText writes keys for help: joined by "/", and three or more single
// characters in a run (1, 2, …, 9) as "1-9".
func KeysText(keys []string) string {
	if len(keys) >= 3 && isRun(keys) {
		return keys[0] + "-" + keys[len(keys)-1]
	}
	return strings.Join(keys, "/")
}

func isRun(keys []string) bool {
	var prev rune
	for i, k := range keys {
		r, n := utf8.DecodeRuneInString(k)
		if n != len(k) || (i > 0 && r != prev+1) {
			return false
		}
		prev = r
	}
	return true
}

// typesText reports whether a canonical key types a character: a
// character or space, with no ctrl or alt.
func typesText(k string) bool {
	if strings.HasPrefix(k, "ctrl+") || strings.HasPrefix(k, "alt+") {
		return false
	}
	return k == "space" || utf8.RuneCountInString(k) == 1
}

var named = map[string]string{
	"enter": "enter", "return": "enter",
	"esc": "esc", "escape": "esc",
	"tab":       "tab",
	"space":     "space",
	"backspace": "backspace",
	"delete":    "delete", "del": "delete",
	"insert": "insert", "ins": "insert",
	"up": "up", "down": "down", "left": "left", "right": "right",
	"home": "home", "end": "end",
	"pgup": "pgup", "pageup": "pgup",
	"pgdown": "pgdown", "pagedown": "pgdown",
}

var modifiers = []string{"ctrl", "alt", "shift"}

// Canonical returns a key's one spelling: modifiers lower case, in the
// order ctrl, alt, shift, joined by "+"; a named key lower case; a
// character as itself, except under ctrl, where a letter is lower case
// (terminals can't tell ctrl+s from ctrl+S). " " is space.
func Canonical(key string) (string, error) {
	if key == " " {
		return "space", nil
	}
	if key == "" {
		return "", errors.New("no key")
	}
	if strings.ContainsFunc(key, unicode.IsSpace) {
		return "", fmt.Errorf("%q: a key has no spaces (write the space bar as space)", key)
	}
	has := map[string]bool{}
	rest := key
	for {
		found := false
		for _, mod := range modifiers {
			// The key after a modifier is never empty: "ctrl+" isn't
			// ctrl with nothing, and "ctrl++" is ctrl with "+".
			if len(rest) > len(mod)+1 && strings.EqualFold(rest[:len(mod)], mod) && (rest[len(mod)] == '+' || rest[len(mod)] == '-') {
				if has[mod] {
					return "", fmt.Errorf("%q: %s twice", key, mod)
				}
				has[mod] = true
				rest = rest[len(mod)+1:]
				found = true
			}
		}
		if !found {
			break
		}
	}
	var base string
	if n, ok := named[strings.ToLower(rest)]; ok {
		base = n
	} else if f, ok := fkey(rest); ok {
		base = f
	} else if r, size := utf8.DecodeRuneInString(rest); size == len(rest) && r != utf8.RuneError && unicode.IsPrint(r) {
		if has["shift"] {
			return "", fmt.Errorf("%q: write the character shift types (%q, not %q)", key, string(unicode.ToUpper(r)), key)
		}
		base = rest
		if has["ctrl"] {
			base = string(unicode.ToLower(r))
		}
	} else {
		return "", fmt.Errorf("%q isn't a key: write a character (a, A, ?), a named key (enter, esc, tab, space, up, pgdown, f1, …) or modifiers before one (ctrl+s, alt+enter, shift+tab)", key)
	}
	var sb strings.Builder
	for _, mod := range modifiers {
		if has[mod] {
			sb.WriteString(mod + "+")
		}
	}
	sb.WriteString(base)
	return sb.String(), nil
}

// fkey reads f1-f20, in any case.
func fkey(s string) (string, bool) {
	if len(s) < 2 || (s[0] != 'f' && s[0] != 'F') {
		return "", false
	}
	n, err := strconv.Atoi(s[1:])
	if err != nil || n < 1 || n > 20 || s[1] == '0' || s[1] == '+' {
		return "", false
	}
	return "f" + strconv.Itoa(n), true
}

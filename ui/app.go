// Package ui is the terminal client's Bubble Tea screens (the design in
// loomux/server, docs/design/tui-client-design.md, "Main workflows"). So
// far it has the home screen: the attention-first list of conversations,
// with its filter and its ? help.
//
// The list polls GET /conversations?limit=100 every 5 s, counted from the
// last answer, and at once on Refresh (the design: "right after any
// action"); the cross-conversation stream (L1) replaces polling later. A
// row's workspace is named from GET /workspaces.
package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
)

// API is what the list asks the server. A *client.Client is one.
type API interface {
	Conversations(ctx context.Context, limit int) (*client.ConversationPage, error)
	Workspaces(ctx context.Context) ([]client.Workspace, error)
}

// Options configure the screens.
type Options struct {
	Profile string      // the profile's name, shown in the header
	API     API         // the server
	Keys    *keymap.Map // the effective keymap (config.Config.Keymap)
	// Now is the clock rows' ages are counted on: time.Now when nil.
	Now func() time.Time
}

const (
	pollEvery      = 5 * time.Second  // the design's list polling
	pollLimit      = 100              // GET /conversations?limit=100
	namesEvery     = time.Minute      // at most one GET /workspaces a minute for a name the list lacks
	namesStale     = 5 * time.Minute  // and one every 5 minutes anyway, for a renamed workspace
	requestTimeout = 15 * time.Second // one poll's limit
	minWidth       = 60               // under 60×15 the screen asks for more room
	minHeight      = 15
)

// App is the root model: the list, its filter's input and its help.
type App struct {
	profile string
	api     API
	keys    *keymap.Map
	now     func() time.Time
	// tick is tea.Tick; tests replace it.
	tick func(time.Duration, func(time.Time) tea.Msg) tea.Cmd

	width, height int
	screen        keymap.Screen // the list, its filter's input or the help
	home          home
	filter        textinput.Model
	helpOffset    int
	notice        string // a line in the footer's place until the next key
	quitting      bool

	poller
}

// New returns the screens for a profile's server.
func New(o Options) *App {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	keys := o.Keys
	if keys == nil {
		keys = keymap.Default()
	}
	in := textinput.New()
	in.Prompt = "/"
	in.SetVirtualCursor(false) // the terminal's own cursor, placed by View
	return &App{
		profile: o.Profile,
		api:     o.API,
		keys:    keys,
		now:     now,
		tick:    tea.Tick,
		screen:  keymap.ListScreen,
		filter:  in,
		poller:  poller{names: map[string]string{}},
	}
}

// Init loads the list and the workspaces' names.
func (a *App) Init() tea.Cmd {
	return tea.Batch(a.poll(), a.fetchNames())
}

// Update handles one message.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.filter.SetWidth(max(1, a.width-3))
	case pageMsg:
		cmd = a.gotPage(msg)
	case namesMsg:
		a.gotNames(msg)
	case pollMsg:
		if msg.gen == a.gen && !a.signedOut {
			cmd = a.poll()
		}
	case refreshMsg:
		if !a.signedOut {
			cmd = a.poll()
		}
	case tea.KeyPressMsg:
		a.notice = ""
		switch a.screen {
		case keymap.FilterScreen:
			cmd = a.filterKey(msg)
		case keymap.HelpScreen:
			cmd = a.helpKey(msg)
		default:
			cmd = a.listKey(msg)
		}
	case tea.PasteMsg:
		if a.screen == keymap.FilterScreen {
			cmd = a.typeFilter(msg)
		}
	}
	a.home.scroll(a.bodyHeight())
	return a, cmd
}

// notYet names the list's actions that later tickets add.
var notYet = map[keymap.Action]string{
	keymap.Open:            "opening a conversation",
	keymap.NewConversation: "starting a conversation",
	keymap.Attach:          "attaching",
	keymap.Watch:           "watching",
	keymap.Browser:         "opening it in the browser",
}

func (a *App) listKey(msg tea.KeyPressMsg) tea.Cmd {
	act, _, ok := a.keys.Lookup(keymap.ListScreen, msg.String())
	if !ok {
		return nil
	}
	page := max(1, a.bodyHeight()-1)
	switch act {
	case keymap.Up:
		a.home.move(-1)
	case keymap.Down:
		a.home.move(1)
	case keymap.PageUp:
		a.home.move(-page)
	case keymap.PageDown:
		a.home.move(page)
	case keymap.Top:
		a.home.move(-len(a.home.visible))
	case keymap.Bottom:
		a.home.move(len(a.home.visible))
	case keymap.Back:
		a.home.setQuery("")
	case keymap.Filter:
		a.screen = keymap.FilterScreen
		a.filter.SetValue(a.home.query)
		a.filter.CursorEnd()
		return a.filter.Focus()
	case keymap.NextGroup:
		a.home.setView(a.home.view.next())
	case keymap.Help:
		a.screen = keymap.HelpScreen
		a.helpOffset = 0
	case keymap.Quit:
		a.quitting = true
		return tea.Quit
	default:
		if what, ok := notYet[act]; ok {
			a.notice = what + " isn't in this version yet"
		}
	}
	return nil
}

// filterKey handles a key while the filter is typed: enter keeps it, esc
// drops it, and anything else edits it, narrowing the list at once.
func (a *App) filterKey(msg tea.KeyPressMsg) tea.Cmd {
	if act, _, ok := a.keys.Lookup(keymap.FilterScreen, msg.String()); ok {
		switch act {
		case keymap.Apply:
			a.home.setQuery(strings.TrimSpace(a.filter.Value()))
		case keymap.Back:
			a.home.setQuery("")
		}
		a.filter.Blur()
		a.screen = keymap.ListScreen
		return nil
	}
	return a.typeFilter(msg)
}

func (a *App) typeFilter(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	a.filter, cmd = a.filter.Update(msg)
	a.home.setQuery(a.filter.Value())
	return cmd
}

func (a *App) helpKey(msg tea.KeyPressMsg) tea.Cmd {
	act, _, ok := a.keys.Lookup(keymap.HelpScreen, msg.String())
	if !ok {
		return nil
	}
	page := max(1, a.bodyHeight()-1)
	switch act {
	case keymap.Back, keymap.Help:
		a.screen = keymap.ListScreen
	case keymap.Quit:
		a.quitting = true
		return tea.Quit
	case keymap.Up:
		a.helpOffset--
	case keymap.Down:
		a.helpOffset++
	case keymap.PageUp:
		a.helpOffset -= page
	case keymap.PageDown:
		a.helpOffset += page
	case keymap.Top:
		a.helpOffset = 0
	case keymap.Bottom:
		a.helpOffset = len(a.helpLines())
	}
	a.helpOffset = max(0, min(a.helpOffset, len(a.helpLines())-a.bodyHeight()))
	return nil
}

// filterShown reports whether the filter's line is on screen: while it's
// typed, and while one is kept.
func (a *App) filterShown() bool {
	return a.screen == keymap.FilterScreen || (a.screen == keymap.ListScreen && a.home.query != "")
}

// bodyHeight is the lines between the header and the footer.
func (a *App) bodyHeight() int {
	h := a.height - 2
	if a.filterShown() {
		h--
	}
	return max(1, h)
}

// View draws the screen.
func (a *App) View() tea.View {
	var v tea.View
	v.AltScreen = true
	switch {
	case a.width == 0 || a.quitting:
		return v
	case a.width < minWidth || a.height < minHeight:
		v.SetContent(ansi.Truncate(fmt.Sprintf("loomux needs at least %d×%d (this is %d×%d)", minWidth, minHeight, a.width, a.height), a.width, "…"))
		return v
	}
	lines := []string{a.header()}
	if a.screen == keymap.HelpScreen {
		lines = append(lines, a.helpBody()...)
	} else {
		lines = append(lines, a.listBody()...)
	}
	if a.filterShown() {
		if a.screen == keymap.FilterScreen {
			lines = append(lines, fit(" "+a.filter.View(), a.width))
			if c := a.filter.Cursor(); c != nil {
				c.X++ // the line's margin
				c.Y = len(lines) - 1
				v.Cursor = c
			}
		} else {
			lines = append(lines, fit(fmt.Sprintf(" filter: %s  (%s clears)", clean(a.home.query), a.firstKey(keymap.Back)), a.width))
		}
	}
	lines = append(lines, a.footer())
	v.SetContent(strings.Join(lines, "\n"))
	return v
}

func (a *App) firstKey(act keymap.Action) string {
	if k := a.keys.Keys(act); len(k) > 0 {
		return k[0]
	}
	return ""
}

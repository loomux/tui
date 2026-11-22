// Package ui is the terminal client's Bubble Tea screens (the design in
// loomux/server, docs/design/tui-client-design.md, "Main workflows"). So
// far it has the home screen: the attention-first list of conversations,
// with its filter and its ? help, attaching to a conversation's agent
// from it (attach.go), and the handoff between clients: o opens a
// conversation on the web, T takes one back from the terminal driving
// it, and Options.Open starts on one (loomux open).
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
	"sync"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
	"github.com/Loomux/tui/internal/browser"
)

// API is what the list asks the server. A *client.Client is one.
type API interface {
	Conversations(ctx context.Context, limit int) (*client.ConversationPage, error)
	Workspaces(ctx context.Context) ([]client.Workspace, error)
	// Conversation and Release take a conversation back.
	Conversation(ctx context.Context, id string, page client.Page) (*client.Conversation, error)
	Release(ctx context.Context, taskID, holdID string) (*client.Release, error)
}

// Options configure the screens.
type Options struct {
	Profile string      // the profile's name, shown in the header
	API     API         // the server
	Keys    *keymap.Map // the effective keymap (config.Config.Keymap)
	// Now is the clock rows' ages are counted on: time.Now when nil.
	Now func() time.Time
	// Attach is how the attach key attaches; without it the key says
	// attaching isn't set up.
	Attach *Attach
	// Server is the profile's server URL: o opens a conversation at
	// <Server>/conversations/<id>, the web's page for it.
	Server string
	// Browse opens a link in the browser: browser.Open when nil.
	Browse func(link string) error
	// Open is a conversation to go to once the list is in (loomux open).
	Open string
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
	profile  string
	api      API
	keys     *keymap.Map
	now      func() time.Time
	server   string
	browseFn func(string) error
	// tick is tea.Tick; tests replace it.
	tick func(time.Duration, func(time.Time) tea.Msg) tea.Cmd

	width, height int
	screen        keymap.Screen // the list, its filter's input or the help
	home          home
	filter        textinput.Model
	helpOffset    int
	notice        string // a line in the footer's place until the next key
	quitting      bool
	opening       string // the conversation to go to once the list is in
	takingBack    bool   // a take back is in flight

	poller

	// Attaching (attach.go). exec is tea.Exec, newWindow opens a tmux
	// window and returns its id, and closeWindow closes one; tests
	// replace them.
	attach      *Attach
	exec        func(tea.ExecCommand, tea.ExecCallback) tea.Cmd
	newWindow   func(ctx context.Context, name string, env, argv []string) (id string, err error)
	closeWindow func(ctx context.Context, id string) error
	starting    *starting      // the attach on its way, if one is
	dialog      *dialog        // its question or text, in the list's place
	held        held           // a hold taken and not yet attached with
	window      opened         // a tmux window opened and not yet shown as such
	unshown     failures       // releases that failed, not yet said
	work        sync.WaitGroup // requests that may leave a hold behind: Close waits for them
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
	browse := o.Browse
	if browse == nil {
		browse = browser.Open
	}
	in := textinput.New()
	in.Prompt = "/"
	in.SetVirtualCursor(false) // the terminal's own cursor, placed by View
	return &App{
		profile:  o.Profile,
		api:      o.API,
		keys:     keys,
		now:      now,
		server:   o.Server,
		browseFn: browse,
		opening:  o.Open,
		tick:     tea.Tick,
		screen:   keymap.ListScreen,
		filter:   in,
		poller:   poller{names: map[string]string{}},

		attach:      o.Attach,
		exec:        tea.Exec,
		newWindow:   tmuxNewWindow,
		closeWindow: tmuxKillWindow,
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
	case browsedMsg:
		a.gotBrowsed(msg)
	case takeBackMsg:
		cmd = a.gotTakeBack(msg)
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
		if handled, c := a.attachKey(msg); handled {
			cmd = c
			break
		}
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
	default:
		cmd = a.attachUpdate(msg)
	}
	a.home.scroll(a.bodyHeight())
	return a, cmd
}

// notYet names the list's actions that later tickets add.
var notYet = map[keymap.Action]string{
	keymap.NewConversation: "starting a conversation",
	keymap.Watch:           "watching",
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
	case keymap.Attach:
		return a.startAttach()
	case keymap.Open:
		if _, ok := a.home.current(); ok {
			return a.openConversation()
		}
	case keymap.Browser:
		return a.browse()
	case keymap.TakeBack:
		return a.takeBack()
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
	switch {
	case a.dialog != nil:
		lines = append(lines, a.dialogBody()...)
	case a.screen == keymap.HelpScreen:
		lines = append(lines, a.helpBody()...)
	default:
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

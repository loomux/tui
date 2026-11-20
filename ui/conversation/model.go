// Package conversation is the conversation screen (the design in
// loomux/server, docs/design/tui-client-design.md, "Chat with a live
// stream"): a conversation's messages, rendered as markdown, kept current
// by its event stream, with the turn in flight and the status of its
// tasks, and a task's transcript one key away.
//
// It is one screen of the client, built to be dropped into the root
// model. The root makes a Model with New, runs the command Init returns,
// forwards every message to Update while the screen is the one shown
// (tea.WindowSizeMsg included) and draws View, which is the whole
// window. The screen asks to be left by sending BackMsg; the root then
// calls Close. A 401 is reported once as SignedOutMsg.
//
// Like every client of the API it keeps no state of its own: what it
// shows is what the server said, in a fetch or on the stream.
package conversation

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
)

const (
	// pageSize is how many messages one fetch asks for: the newest, or
	// the page before the oldest one loaded.
	pageSize = 50
	// reconnectingAfter is how long the stream stays down before the
	// header says so (the design's "Error handling").
	reconnectingAfter = 5 * time.Second
	// fetchTimeout ends a fetch that never answers: until it does, no
	// other is sent.
	fetchTimeout = 30 * time.Second
)

// API is what the screen asks of the server. *client.Client is one.
type API interface {
	Conversation(ctx context.Context, id string, page client.Page) (*client.Conversation, error)
	Transcript(ctx context.Context, taskID string, page client.Page) (*client.Transcript, error)
	OpenStream(conversationID string) *client.Stream
}

// Options is what a screen is made from.
type Options struct {
	// Client is the signed-in client.
	Client API
	// ID is the conversation's id.
	ID string
	// Profile names the profile in the header, as the list does.
	Profile string
	// Title names the conversation in the header: its workspace, or
	// what the list shows for it. Without one, the start of the id.
	Title string
	// Keys is the effective keymap; the defaults when nil.
	Keys *keymap.Map
	// Width and Height are the window's size, when the root knows it. A
	// tea.WindowSizeMsg changes it.
	Width, Height int
	// Now is the clock message times are told by, in its location;
	// time.Now when nil.
	Now func() time.Time
}

// BackMsg is sent when the user leaves the screen (back). The root
// shows what was there before and calls Close.
type BackMsg struct{}

// SignedOutMsg is sent, once, when the server answers 401: the token is
// unknown, revoked or expired. The screen has stopped; what happens to
// the stored token and what is shown next are the root's.
type SignedOutMsg struct{ Err error }

// eventStream is the conversation's stream as the screen reads it:
// *client.Stream.
type eventStream interface {
	Next(context.Context) (client.StreamEvent, error)
	Close() error
}

// instances numbers the screens made, so that an answer still on its way
// when the root opens another conversation isn't taken for the new
// screen's.
var instances atomic.Uint64

// Messages the screen sends itself.
type (
	// loadedMsg is a fetch's answer: the newest page, or (older) the
	// page before the cursor it was asked with.
	loadedMsg struct {
		inst   uint64
		conv   *client.Conversation
		err    error
		older  bool
		before string
	}
	// streamMsg is what the stream's Next returned.
	streamMsg struct {
		inst uint64
		ev   client.StreamEvent
		err  error
	}
	// dropMsg says a drop of the stream has lasted reconnectingAfter.
	dropMsg struct {
		inst uint64
		drop int
	}
)

// Model is the conversation screen.
type Model struct {
	api     API
	id      string
	inst    uint64
	profile string
	title   string
	keys    *keymap.Map
	now     func() time.Time
	// open and after are OpenStream and tea.Tick: tests put a scripted
	// stream and a timer they fire themselves in their place.
	open  func(string) eventStream
	after func(time.Duration, func(time.Time) tea.Msg) tea.Cmd

	// ctx ends with the screen, and with it every request in flight.
	ctx    context.Context
	cancel context.CancelFunc
	stream eventStream
	closed bool

	width, height int

	st     state
	loaded bool // a fetch has answered
	// fetching says a fetch of the newest page is in flight; stale that
	// the conversation changed since it was asked, so one more follows.
	fetching bool
	stale    bool
	// err is why the last fetch failed; nil once one answers.
	err error
	// olderFetching and olderErr are the same for the page of older
	// messages.
	olderFetching bool
	olderErr      error
	signedOut     bool

	// The stream: connected says it has been open, down that it isn't
	// now; drop counts the drops, and reconnecting says this one has
	// lasted reconnectingAfter. streamErr is an error that ended it.
	connected    bool
	down         bool
	drop         int
	reconnecting bool
	streamErr    error

	md *markdown
	// cache holds each message's lines as rendered at the width.
	cache map[string][]string
	// body is the transcript; follow says it stays on the newest
	// message as more arrive.
	body   viewport.Model
	follow bool
	// status is the lines under the transcript: the tasks.
	status []string
	// notice replaces the footer until the next key.
	notice string

	// tr is the task transcript shown over the conversation, when one
	// is; trSeq counts the ones opened.
	tr    *transcript
	trSeq int

	help     bool
	helpBody viewport.Model
}

// New returns the screen of conversation o.ID. It does nothing until the
// command Init returns is run.
func New(o Options) *Model {
	m := &Model{
		api:     o.Client,
		id:      o.ID,
		inst:    instances.Add(1),
		profile: line(o.Profile),
		title:   line(o.Title),
		keys:    o.Keys,
		now:     o.Now,
		after:   tea.Tick,
		width:   o.Width,
		height:  o.Height,
		md:      newMarkdown(),
		cache:   map[string][]string{},
		body:    viewport.New(),
		follow:  true,

		helpBody: viewport.New(),
	}
	m.open = func(id string) eventStream { return m.api.OpenStream(id) }
	if m.keys == nil {
		m.keys = keymap.Default()
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.title == "" {
		m.title = line(m.id)
		if r := []rune(m.title); len(r) > 8 {
			m.title = string(r[:8])
		}
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.render()
	return m
}

// Init fetches the conversation and opens its stream. It also asks the
// terminal for its background colour, which chooses the markdown style.
func (m *Model) Init() tea.Cmd {
	m.stream = m.open(m.id)
	return tea.Batch(m.fetch(), m.next(), tea.RequestBackgroundColor)
}

// Close ends the stream and the requests in flight. The screen takes no
// more messages. Closing twice is harmless.
func (m *Model) Close() {
	m.closed = true
	m.stop()
}

// stop ends what the screen has running.
func (m *Model) stop() {
	m.cancel()
	if m.stream != nil {
		m.stream.Close()
	}
}

// Update takes the next message. The root forwards every message while
// the screen is shown; the ones that aren't this screen's are ignored.
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	if m.closed {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width != m.width {
			clear(m.cache)
		}
		m.width, m.height = msg.Width, msg.Height
		m.render()
	case tea.BackgroundColorMsg:
		m.md.setDark(msg.IsDark())
		clear(m.cache)
		m.render()
	case tea.KeyPressMsg:
		return m, m.onKey(msg)
	case loadedMsg:
		if msg.inst == m.inst && !m.signedOut {
			return m, m.onLoaded(msg)
		}
	case streamMsg:
		if msg.inst == m.inst && !m.signedOut {
			return m, m.onStream(msg)
		}
	case turnsMsg:
		if msg.inst == m.inst && !m.signedOut {
			return m, m.onTurns(msg)
		}
	case dropMsg:
		if msg.inst == m.inst && msg.drop == m.drop && m.down {
			m.reconnecting = true
		}
	}
	return m, nil
}

// fetch asks for the newest page. One fetch is in flight at a time: an
// answer shows the conversation as it was when it was asked, so two in
// flight could land in the wrong order. A change while one is in flight
// asks for one more after it.
func (m *Model) fetch() tea.Cmd {
	if m.fetching {
		m.stale = true
		return nil
	}
	m.fetching, m.stale = true, false
	return m.get(client.Page{Limit: pageSize}, false)
}

// fetchOlder asks for the page before the oldest message loaded, if
// there is one and it isn't on its way already.
func (m *Model) fetchOlder() tea.Cmd {
	if !m.st.hasMore || m.olderFetching || m.signedOut {
		return nil
	}
	m.olderFetching, m.olderErr = true, nil
	m.render()
	return m.get(client.Page{Before: m.st.nextBefore, Limit: pageSize}, true)
}

func (m *Model) get(page client.Page, older bool) tea.Cmd {
	parent, api, id, inst := m.ctx, m.api, m.id, m.inst
	return func() tea.Msg {
		ctx, cancel := contextWithTimeout(parent)
		defer cancel()
		conv, err := api.Conversation(ctx, id, page)
		return loadedMsg{inst: inst, conv: conv, err: err, older: older, before: page.Before}
	}
}

func contextWithTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, fetchTimeout)
}

func (m *Model) onLoaded(msg loadedMsg) tea.Cmd {
	if client.Unauthorized(msg.err) {
		return m.signOut(msg.err)
	}
	if msg.older {
		m.olderFetching, m.olderErr = false, msg.err
		if msg.err != nil {
			m.render()
			return nil
		}
		// The view stays on the line it was on: the page goes above it.
		total, offset := m.body.TotalLineCount(), m.body.YOffset()
		joined := m.st.applyOlder(msg.before, msg.conv)
		m.render()
		if joined && !m.follow {
			m.body.SetYOffset(offset + m.body.TotalLineCount() - total)
		}
		return nil
	}
	m.fetching = false
	switch {
	case msg.err == nil:
		m.err, m.loaded = nil, true
		m.st.applyNewest(msg.conv)
	case client.Is(msg.err, client.CodeNotFound) && len(m.st.messages) == 0:
		// A conversation opened before its first message: the server
		// has nothing under its id yet.
		m.err, m.loaded = nil, true
	default:
		m.err = msg.err
	}
	m.render()
	if m.stale {
		return m.fetch()
	}
	return nil
}

// next waits for the stream's next event. Every event asks for the one
// after it, so the stream is read by one command at a time.
func (m *Model) next() tea.Cmd {
	ctx, s, inst := m.ctx, m.stream, m.inst
	return func() tea.Msg {
		ev, err := s.Next(ctx)
		return streamMsg{inst: inst, ev: ev, err: err}
	}
}

func (m *Model) onStream(msg streamMsg) tea.Cmd {
	switch {
	case msg.err == nil:
		return tea.Batch(m.onEvent(msg.ev), m.next())
	case client.Unauthorized(msg.err):
		return m.signOut(msg.err)
	case errors.Is(msg.err, client.ErrStreamClosed), errors.Is(msg.err, context.Canceled):
		// The screen's own doing.
	default:
		m.streamErr = msg.err
	}
	return nil
}

func (m *Model) onEvent(ev client.StreamEvent) tea.Cmd {
	var fetch bool
	var turns tea.Cmd
	switch ev.Type {
	case client.EventConnected:
		m.connected, m.down, m.reconnecting = true, false, false
		// Nothing added before the stream opened is replayed: not what
		// came between the first fetch and the first connect, and not
		// what came during a drop. That goes for the turns of an open
		// transcript too.
		if m.tr != nil {
			turns = m.fetchTurns()
		}
		return tea.Batch(turns, m.fetch())
	case client.EventDisconnected:
		if m.down {
			return nil // a retry that failed: the same drop
		}
		m.down = true
		m.drop++
		inst, drop := m.inst, m.drop
		return m.after(reconnectingAfter, func(time.Time) tea.Msg { return dropMsg{inst: inst, drop: drop} })
	case client.EventMessageAdded:
		// The event has no content: the message comes with the page.
		fetch = true
	case client.EventTaskUpdate:
		fetch = ev.Task != nil && m.st.taskUpdate(*ev.Task)
		// A turn joins the transcript as it ends, which is a change of
		// the task's status.
		if fetch && m.tr != nil && m.tr.task.ID == ev.Task.TaskID {
			turns = m.fetchTurns()
		}
	case client.EventDispatchUpdate:
		fetch = ev.Dispatch != nil && m.st.dispatchUpdate(*ev.Dispatch)
	default:
		return nil // an event of a later server
	}
	// An event that didn't decode still says something changed.
	fetch = fetch || ev.Err != nil
	m.render()
	if fetch || m.fetching {
		return tea.Batch(turns, m.fetch())
	}
	return turns
}

// signOut stops the screen and tells the root, once.
func (m *Model) signOut(err error) tea.Cmd {
	m.signedOut = true
	m.fetching, m.olderFetching = false, false
	m.stop()
	m.render()
	return func() tea.Msg { return SignedOutMsg{Err: err} }
}

// later names the actions of this screen that later tickets build, as
// the notice for one says it.
var later = map[keymap.Action]string{
	keymap.Attach:  "attaching",
	keymap.Watch:   "watching",
	keymap.Browser: "opening it in the browser",
	keymap.Cancel:  "cancelling a turn",
	keymap.Yes:     "answering a prompt",
	keymap.No:      "answering a prompt",
	keymap.Choose:  "answering a prompt",
	keymap.Reply:   "replying",
}

func (m *Model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	switch {
	case m.help:
		return m.onHelpKey(msg)
	case m.tr != nil:
		return m.onTranscriptKey(msg)
	}
	action, _, ok := m.keys.Lookup(keymap.ConversationScreen, msg.String())
	if !ok {
		return nil
	}
	if moved, top := scroll(&m.body, action); moved {
		m.follow = m.body.AtBottom()
		if top {
			return m.fetchOlder()
		}
		return nil
	}
	switch action {
	case keymap.Back:
		m.Close()
		return func() tea.Msg { return BackMsg{} }
	case keymap.Quit:
		m.Close()
		return tea.Quit
	case keymap.Help:
		m.openHelp()
	case keymap.Transcript:
		return m.openTranscript()
	default:
		if what, ok := later[action]; ok {
			m.notice = what + " isn't in this version yet"
		}
	}
	return nil
}

// openHelp shows the keys of the screen it is opened from.
func (m *Model) openHelp() {
	m.help = true
	m.render()
	m.helpBody.GotoTop()
}

func (m *Model) onHelpKey(msg tea.KeyPressMsg) tea.Cmd {
	action, _, ok := m.keys.Lookup(keymap.HelpScreen, msg.String())
	if !ok {
		return nil
	}
	if moved, _ := scroll(&m.helpBody, action); moved {
		return nil
	}
	switch action {
	case keymap.Back, keymap.Help:
		m.help = false
	case keymap.Quit:
		m.Close()
		return tea.Quit
	}
	return nil
}

// scroll moves a viewport as a navigation action says. It reports
// whether the action was one, and whether it went up and is at the top:
// the cue to load what is older. (A text that fits the window is at its
// top and at its end at once, so being at the top isn't enough.)
func scroll(v *viewport.Model, action keymap.Action) (moved, top bool) {
	up := false
	switch action {
	case keymap.Up:
		v.ScrollUp(1)
		up = true
	case keymap.Down:
		v.ScrollDown(1)
	case keymap.PageUp:
		v.PageUp()
		up = true
	case keymap.PageDown:
		v.PageDown()
	case keymap.Top:
		v.GotoTop()
		up = true
	case keymap.Bottom:
		v.GotoBottom()
	default:
		return false, false
	}
	return true, up && v.AtTop()
}

// line is s as one line of clean text.
func line(s string) string {
	return strings.Join(strings.Fields(clean(s)), " ")
}

package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/attach"
	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config/keymap"
)

// Attaching from the screens (the design's "Attach to a live agent and
// detach" and "Suspending and resuming the TUI"). The attach key on a
// conversation attaches to its open agent task, in one of two ways:
//
//   - inline: the screens take the hold, hand the terminal to the attach
//     routine (package attach) and come back when it has released the
//     hold. The hold's heartbeat is the routine's own goroutine, so it
//     runs while the screens are suspended.
//   - window, inside tmux: a new tmux window runs `loomux attach --task
//     ID`, which takes, renews and releases the hold on its own. The
//     screens stay up in their window.
//
// What can't be attached to is said on screen: who holds the task, with a
// question before it is taken from them; the server's command as text for
// a server that can't attach yet; the commands and why when this machine
// can run none.

// AttachAPI is what attaching asks the server. A *client.Client is one.
type AttachAPI interface {
	attach.API
	Conversation(ctx context.Context, id string, page client.Page) (*client.Conversation, error)
}

// The attach modes: the config's [attach] mode.
const (
	AttachAuto   = "auto"   // a window inside tmux, else inline
	AttachInline = "inline" // always on this terminal
	AttachWindow = "window" // a tmux window; without tmux, inline
)

// Attach is how the attach key attaches.
type Attach struct {
	API AttachAPI
	// Config chooses among the server's attach commands.
	Config attach.Config
	// Mode is AttachAuto (also when empty), AttachInline or AttachWindow.
	Mode string
	// InTmux says the screens run inside tmux: there is a tmux to open a
	// window in.
	InTmux bool
	// Window is the command a tmux window runs to attach, up to the task:
	// this program, "attach" and the profile. "--task ID" is added.
	Window []string
	// WindowEnv are the NAME=value settings of this program's environment
	// that the window's command must have as they are here: a tmux window
	// starts from the tmux server's environment, not from this program's.
	// One that is unset here is given empty, so the tmux server's own
	// value for it isn't used.
	WindowEnv []string
}

// starting is an attach on its way: from the key until the terminal is
// handed over or a window is open. One at a time.
type starting struct {
	// ctx bounds its requests. It is cancelled only when the start has
	// settled, or when Close has waited long enough: a takeover on its
	// way is left to finish, because its answer is the only way to learn
	// the hold to give back.
	ctx    context.Context
	cancel context.CancelFunc
	// cancelled: esc stopped it, and what its request in flight answers
	// is only cleaned up.
	cancelled bool

	conv  string // the conversation
	name  string // its workspace's name: the window's
	ws    string // its conversation's workspace id
	task  string // its task, once known
	force bool   // take the task from the session that holds it
	// waitName: attachNow stopped for the names: gotNames goes on with it.
	waitName bool
	// preface is a line for the terminal before the attach's own: why it
	// attaches here and not in a window.
	preface string
}

// held keeps the session of a hold taken and not yet attached with, so
// that whatever ends the screens first can give it back.
type held struct {
	mu   sync.Mutex
	sess *attach.Session
}

func (h *held) put(s *attach.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sess = s
}

func (h *held) take() *attach.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sess
	h.sess = nil
	return s
}

// opened keeps the id of a tmux window opened and not yet shown as such
// by the list, so that whatever ends the screens first can close it
// again.
type opened struct {
	mu sync.Mutex
	id string
}

func (o *opened) put(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.id = id
}

func (o *opened) take() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	id := o.id
	o.id = ""
	return id
}

// failures keeps the releases that didn't reach the server until the
// screens have said them, or, if the screens end first, until Close
// returns them: such a hold stays until its lease runs out, which the
// person has to know. Each is said once.
type failures struct {
	mu   sync.Mutex
	errs []*attach.ReleaseError
}

// put keeps rel until it is said.
func (f *failures) put(rel *attach.ReleaseError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs = append(f.errs, rel)
}

// said drops rel: the screens have shown it.
func (f *failures) said(rel *attach.ReleaseError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs = slices.DeleteFunc(f.errs, func(e *attach.ReleaseError) bool { return e == rel })
}

// take returns the failures not yet said, and drops them.
func (f *failures) take() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	errs := make([]error, len(f.errs))
	for i, e := range f.errs {
		errs[i] = e
	}
	f.errs = nil
	return errors.Join(errs...)
}

// dialog is text in the list's place: what an attach has to say that a
// footer line can't hold. With yes it is a question: the yes key answers
// it, any other key is a no. Without, it is text to read: the list's
// movement keys scroll it when it doesn't fit, and any other key closes
// it.
type dialog struct {
	lines []string
	yes   func() tea.Cmd
	no    func()
	// offset is the first of its wrapped lines on screen.
	offset int
}

type (
	// taskMsg is the conversation's task, found.
	taskMsg struct {
		st   *starting
		task client.Task
		ok   bool
		err  error
	}
	// windowMsg is tmux's answer to the new window. Its id is in
	// App.window.
	windowMsg struct {
		st  *starting
		err error
	}
	// takenMsg is the takeover's answer. The session is in App.held.
	takenMsg struct {
		st  *starting
		err error
	}
	// gaveBackMsg is the release of a hold taken for an attach that isn't
	// happening: rel if it failed, also kept in App.unshown until said,
	// after lead.
	gaveBackMsg struct {
		rel  *attach.ReleaseError
		lead string
	}
	// attachedMsg is the attach, over: the terminal is the screens' again.
	attachedMsg struct {
		res attach.Result
		err error
		// sig is the signal that ended it, if one did.
		sig os.Signal
		// notRun: the terminal couldn't be handed over, so the attach
		// didn't run (err says why), and its hold is still held.
		notRun bool
	}
)

// startAttach is the attach key on the list.
func (a *App) startAttach() tea.Cmd {
	it, ok := a.home.current()
	switch {
	case !ok:
		return nil
	case a.attach == nil || a.attach.API == nil:
		a.notice = "attaching isn't set up"
		return nil
	case a.starting != nil:
		return nil // one at a time
	}
	ctx, cancel := context.WithCancel(context.Background())
	st := &starting{ctx: ctx, cancel: cancel, conv: it.ConversationID, name: it.workspace, ws: it.WorkspaceID}
	a.starting = st
	a.notice = "attaching…"
	api := a.attach.API
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(st.ctx, requestTimeout)
		defer cancel()
		conv, err := api.Conversation(ctx, st.conv, client.Page{Limit: 1})
		if err != nil {
			return taskMsg{st: st, err: err}
		}
		task, ok := attach.TaskOf(conv.Tasks)
		return taskMsg{st: st, task: task, ok: ok}
	}
}

// settle ends a start: another may begin.
func (a *App) settle(st *starting) {
	if a.starting == st {
		a.starting = nil
	}
	st.cancel()
}

// attachUpdate handles the attach's own messages.
func (a *App) attachUpdate(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case taskMsg:
		return a.gotTask(msg)
	case windowMsg:
		return a.gotWindow(msg)
	case takenMsg:
		return a.gotTaken(msg)
	case attachedMsg:
		return a.gotAttached(msg)
	case gaveBackMsg:
		if msg.rel != nil {
			a.unshown.said(msg.rel)
			a.notice = msg.lead + clean(msg.rel.Error())
		}
	}
	return nil
}

func (a *App) gotTask(msg taskMsg) tea.Cmd {
	st := msg.st
	if st != a.starting {
		return nil
	}
	switch {
	case st.cancelled:
		a.settle(st)
		return nil
	case msg.err != nil:
		a.settle(st)
		a.notice = "couldn't attach: " + clean(msg.err.Error())
		return nil
	case !msg.ok:
		a.settle(st)
		a.notice = "no agent to attach to: none of this conversation's agents is open"
		return nil
	}
	st.task = msg.task.ID
	return a.attachNow(st)
}

// attachNow goes on with a start whose task is known: in a window of its
// own, if one can be opened, or here.
func (a *App) attachNow(st *starting) tea.Cmd {
	at := a.attach
	switch {
	case at.Mode == AttachInline || (at.Mode != AttachWindow && !at.InTmux):
	case !at.InTmux:
		st.preface = "not inside tmux, so there is no window to open: attaching here"
	case len(at.Window) == 0:
		st.preface = "no command for a tmux window: attaching here"
	default:
		// The names may have answered since a was pressed: the window
		// takes the name that arrived, not the stale one the row had.
		if st.ws != "" && a.names[st.ws] != "" {
			st.name = workspaceName(st.ws, a.names)
		}
		// If the names are still on their way (the list asked for them),
		// wait for that answer rather than name the window after the
		// start of the id; when none is coming, the id's start stands.
		if st.ws != "" && a.names[st.ws] == "" && a.namesBusy {
			st.waitName = true
			return nil
		}
		return a.openWindow(st)
	}
	return a.take(st)
}

// openWindow opens the attach's own tmux window.
func (a *App) openWindow(st *starting) tea.Cmd {
	name := st.name
	argv := append(append([]string(nil), a.attach.Window...), "--task", st.task)
	env, open := a.attach.WindowEnv, a.newWindow
	a.work.Add(1)
	return func() tea.Msg {
		defer a.work.Done()
		ctx, cancel := context.WithTimeout(st.ctx, requestTimeout)
		defer cancel()
		id, err := open(ctx, name, env, argv)
		if err == nil {
			a.window.put(id)
		}
		return windowMsg{st, err}
	}
}

func (a *App) gotWindow(msg windowMsg) tea.Cmd {
	st := msg.st
	if st != a.starting {
		return nil
	}
	switch {
	case st.cancelled:
		a.settle(st)
		// It may have opened all the same. Closed again, its `loomux
		// attach` is hung up, and gives back the hold if it had taken
		// it: nothing attaches after the list said the attach was
		// cancelled.
		id, closeWindow := a.window.take(), a.closeWindow
		if id == "" {
			return nil
		}
		a.work.Add(1)
		return func() tea.Msg {
			defer a.work.Done()
			ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
			defer cancel()
			_ = closeWindow(ctx, id)
			return nil
		}
	case msg.err != nil:
		// Not the end of the attach: here, then.
		st.preface = "couldn't open a tmux window (" + clean(msg.err.Error()) + "): attaching here"
		return a.take(st)
	}
	// The window is the person's from here: it is shown as open.
	a.window.take()
	a.settle(st)
	a.notice = "attaching in a new tmux window; it hands the agent back when you detach"
	return Refresh
}

// windowName is a workspace's name as tmux new-window's -n takes it. tmux
// reads -n as a format, and a format can run a shell command: #(command).
// The name is the server's text, so every # is doubled, which tmux reads
// as the character itself. It is cut to 40 characters: a window's name is
// a label.
func windowName(name string) string {
	if name == "" {
		name = "loomux"
	}
	if r := []rune(name); len(r) > 40 {
		name = string(r[:40])
	}
	return strings.ReplaceAll(name, "#", "##")
}

// tmuxNewWindow opens a window of the tmux this program runs in, named
// name, running argv directly (tmux runs several arguments without a
// shell, and reads neither them nor the environment as formats) with env
// set in its environment. It returns the window's id.
func tmuxNewWindow(ctx context.Context, name string, env, argv []string) (string, error) {
	args := []string{"new-window", "-P", "-F", "#{window_id}", "-n", windowName(name)}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	args = append(append(args, "--"), argv...)
	out, err := tmux(ctx, args...)
	return strings.TrimSpace(out), err
}

// tmuxKillWindow closes the window with id, as tmuxNewWindow returned
// it.
func tmuxKillWindow(ctx context.Context, id string) error {
	_, err := tmux(ctx, "kill-window", "-t", id)
	return err
}

// tmux runs a command of the tmux this program runs in. Its error is
// tmux's own first line.
func tmux(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "tmux", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n"); line != "" {
			return "", fmt.Errorf("tmux %s: %s", args[0], line)
		}
		return "", fmt.Errorf("tmux %s: %w", args[0], err)
	}
	return string(out), nil
}

// take takes the hold. The session waits in a.held for gotTaken, where
// Close finds it if the screens end first. So does a hold Take took and
// couldn't give back, in a.unshown.
func (a *App) take(st *starting) tea.Cmd {
	at, force := a.attach, st.force
	a.work.Add(1)
	return func() tea.Msg {
		defer a.work.Done()
		ctx, cancel := context.WithTimeout(st.ctx, requestTimeout)
		defer cancel()
		sess, err := attach.Take(ctx, at.API, st.task, attach.Options{Config: at.Config, Force: force})
		if err == nil {
			a.held.put(sess)
		}
		var rel *attach.ReleaseError
		if errors.As(err, &rel) {
			a.unshown.put(rel)
		}
		return takenMsg{st, err}
	}
}

func (a *App) gotTaken(msg takenMsg) tea.Cmd {
	st := msg.st
	if st != a.starting {
		return nil // a failed release in it is Close's to say
	}
	// A hold the takeover took and couldn't give back is said here: in
	// the footer after a cancel, else with the rest of the error.
	var rel *attach.ReleaseError
	if errors.As(msg.err, &rel) {
		a.unshown.said(rel)
	}
	if st.cancelled {
		a.settle(st)
		if rel != nil {
			a.notice = "attach cancelled, but " + clean(rel.Error())
		}
		return a.giveBack("attach cancelled, but ")
	}
	if msg.err != nil {
		var held *client.Error
		if errors.As(msg.err, &held) && held.Code == client.CodeTaskHeld && !st.force {
			a.askHeld(st, held.Hold)
			return nil
		}
		a.settle(st)
		a.showAttachError(msg.err)
		return nil
	}
	// The terminal goes to the attach routine, which takes the session
	// from a.held when it starts: until then the hold is Close's to give
	// back.
	e := &attachExec{held: &a.held, preface: st.preface}
	return a.exec(e, func(err error) tea.Msg {
		if err != nil && e.done.err == nil {
			e.done.err = err // the terminal couldn't be released or restored
		}
		// Bubble Tea skips Run when it can't release the terminal.
		e.done.notRun = !e.ran
		return e.done
	})
}

// giveBack releases a hold taken for an attach that isn't happening. A
// release that fails is said in the footer, after lead.
func (a *App) giveBack(lead string) tea.Cmd {
	sess := a.held.take()
	if sess == nil {
		return nil
	}
	a.work.Add(1)
	return func() tea.Msg {
		defer a.work.Done()
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		var rel *attach.ReleaseError
		if errors.As(sess.Release(ctx), &rel) {
			a.unshown.put(rel) // for the footer, or for Close if the screens end first
		}
		return gaveBackMsg{rel, lead}
	}
}

// askHeld puts the question about a task another session holds: taking
// it detaches that client, so it is the person's call.
func (a *App) askHeld(st *starting, hold *client.Hold) {
	a.dialog = &dialog{
		lines: []string{
			sentence(attach.HeldLine(hold)),
			"",
			"Take it over? That client is detached, and the agent is yours to drive.",
		},
		yes: func() tea.Cmd {
			st.force = true
			a.notice = "attaching…"
			return a.take(st)
		},
		no: func() {
			a.settle(st)
			a.notice = "left with its holder"
		},
	}
}

// showAttachError says why an attach didn't start: the server's commands
// as text where the person may want to run one by hand, and a hold taken
// and not given back with when it runs out; else a line.
func (a *App) showAttachError(err error) {
	var (
		old  *attach.UnsupportedError
		none *attach.NoCommandError
		rel  *attach.ReleaseError
	)
	switch {
	case errors.As(err, &old):
		lines := []string{
			"This server can't attach yet (it has no takeover): upgrade it.",
			"",
			"Until then, to attach by hand (Loomux won't know someone is in the pane, and may type into it):",
			"",
		}
		for _, c := range old.Plan.Commands {
			lines = append(lines, "  "+c.String(), "")
		}
		a.dialog = &dialog{lines: lines}
	case errors.As(err, &none) && len(none.Skipped) > 0:
		lines := []string{"No attach command can be run from this machine. Nothing was taken.", ""}
		if errors.As(err, &rel) {
			// The commands came with a hold, which couldn't be given back.
			lines = []string{"No attach command can be run from this machine.", "", sentence(clean(rel.Error())), ""}
		}
		for _, s := range none.Skipped {
			lines = append(lines, "  "+s.Line, "    "+s.Reason, "")
		}
		a.dialog = &dialog{lines: lines}
	case errors.As(err, &rel):
		// A hold taken and not given back (attach.Take joins the two): more
		// than the footer holds.
		var lines []string
		for _, e := range joined(err) {
			text := e.Error()
			if e != error(rel) {
				text = "couldn't attach: " + text
			}
			lines = append(lines, sentence(clean(text)), "")
		}
		a.dialog = &dialog{lines: lines}
	default:
		a.notice = "couldn't attach: " + clean(err.Error())
	}
}

// joined is err's errors one by one, for an error that joins several
// (errors.Join), else err.
func joined(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

// sentence is s as a sentence of its own: a capital first letter and a
// full stop.
func sentence(s string) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[n:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func (a *App) gotAttached(msg attachedMsg) tea.Cmd {
	if st := a.starting; st != nil {
		a.settle(st)
	}
	if msg.notRun {
		// The terminal stayed the screens': the hold taken for the attach
		// is given back.
		a.notice = "couldn't attach: the terminal couldn't be handed over"
		if msg.err != nil {
			a.notice = "couldn't attach: " + clean(msg.err.Error())
		}
		return a.giveBack("couldn't attach, and ")
	}
	// A signal while the attach had the terminal ended the attach, which
	// released the hold on its way out, or says why it couldn't. An
	// interrupt is the person's ctrl+c at the command (ssh asking for a
	// password): the list is back. Any other told the client itself to
	// stop.
	var rel *attach.ReleaseError
	if msg.sig != nil && msg.sig != os.Interrupt {
		// A release that failed on the way out is said once the screens
		// have ended: Close returns it.
		if errors.As(msg.err, &rel) {
			a.unshown.put(rel)
		}
		a.quitting = true
		return tea.Quit
	}
	switch {
	case msg.sig != nil && errors.As(msg.err, &rel):
		// The release after the interrupt didn't reach the server: the
		// hold stays until its lease runs out.
		a.notice = "attach interrupted: " + clean(rel.Error())
	case msg.sig != nil && msg.err != nil && !errors.Is(msg.err, context.Canceled):
		// Something else ended it too, such as the hold ending another way.
		a.notice = "attach interrupted: " + clean(msg.err.Error())
	case msg.sig != nil:
		a.notice = "attach interrupted: the hold is released"
	case msg.err != nil:
		a.notice = clean(msg.err.Error())
	case msg.res.Status != "":
		a.notice = "detached: the hold is released and the task is " + clean(msg.res.Status)
	default:
		a.notice = "detached: the hold is released"
	}
	return Refresh
}

// attachKey handles a key the attach has first call on: a dialog's
// answer, and esc while an attach is starting.
func (a *App) attachKey(msg tea.KeyPressMsg) (handled bool, cmd tea.Cmd) {
	if d := a.dialog; d != nil {
		if d.yes == nil {
			if a.scrollDialog(msg) {
				return true, nil
			}
			a.dialog = nil
			return true, nil
		}
		a.dialog = nil
		// The yes key is the keymap's, as on a conversation's cards.
		if act, _, ok := a.keys.Lookup(keymap.ConversationScreen, msg.String()); ok && act == keymap.Yes {
			return true, d.yes()
		}
		d.no()
		return true, nil
	}
	if st := a.starting; st != nil && !st.cancelled {
		if act, _, ok := a.keys.Lookup(keymap.ListScreen, msg.String()); ok && act == keymap.Back {
			// Its request in flight is left to finish (see starting.ctx);
			// what it answers is cleaned up.
			st.cancelled = true
			a.notice = "attach cancelled"
			if st.waitName {
				// Nothing is in flight but the names, and their answer
				// only updates the list: settle now, so a later attach
				// key starts another start instead of being ignored.
				a.settle(st)
			}
			return true, nil
		}
	}
	return false, nil
}

// dialogLines is the dialog's text, wrapped to the width.
func (a *App) dialogLines() []string {
	lines := []string{""}
	for _, l := range a.dialog.lines {
		indent := len(l) - len(strings.TrimLeft(l, " "))
		for _, w := range strings.Split(ansi.Wrap(l, max(8, a.width-2-indent), ""), "\n") {
			lines = append(lines, " "+strings.Repeat(" ", indent)+strings.TrimLeft(w, " "))
		}
	}
	return lines
}

// dialogOverflow is how many of the dialog's lines don't fit the screen:
// how far it scrolls.
func (a *App) dialogOverflow() int {
	return max(0, len(a.dialogLines())-a.bodyHeight())
}

// dialogBody is the part of the dialog's lines on screen, in the list's
// place.
func (a *App) dialogBody() []string {
	height := a.bodyHeight()
	lines := a.dialogLines()
	a.dialog.offset = max(0, min(a.dialog.offset, a.dialogOverflow()))
	lines = lines[a.dialog.offset:]
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

// scrollDialog scrolls a text that doesn't fit by the list's movement
// keys. It reports false for any other key, and for a text that fits:
// that key closes it.
func (a *App) scrollDialog(msg tea.KeyPressMsg) bool {
	over := a.dialogOverflow()
	if over == 0 {
		return false
	}
	act, _, ok := a.keys.Lookup(keymap.ListScreen, msg.String())
	if !ok {
		return false
	}
	d, page := a.dialog, max(1, a.bodyHeight()-1)
	switch act {
	case keymap.Up:
		d.offset--
	case keymap.Down:
		d.offset++
	case keymap.PageUp:
		d.offset -= page
	case keymap.PageDown:
		d.offset += page
	case keymap.Top:
		d.offset = 0
	case keymap.Bottom:
		d.offset = over
	default:
		return false
	}
	d.offset = max(0, min(d.offset, over))
	return true
}

func (a *App) dialogFooter() string {
	key := func(act keymap.Action) string {
		if k := a.keys.Keys(act); len(k) > 0 {
			return k[0]
		}
		return "?"
	}
	switch {
	case a.dialog.yes != nil:
		return faint.Render(fit(" "+bold.Render(key(keymap.Yes))+" take it over  "+bold.Render(key(keymap.No))+" leave it", a.width))
	case a.dialogOverflow() > 0:
		return faint.Render(fit(" "+bold.Render(key(keymap.Down))+"/"+bold.Render(key(keymap.Up))+" more  any other key closes this", a.width))
	}
	return faint.Render(fit(" any key closes this", a.width))
}

// closeWait is how long Close lets a takeover on its way finish before
// giving up on it.
const closeWait = 5 * time.Second

// Close ends what an attach left behind when the screens end: it gives
// back a hold taken and not yet attached with, and closes a tmux window
// opened and not yet shown as such. It also closes a conversation still
// open. Call it once the program has stopped.
//
// A takeover on its way to the server is waited for, closeWait at most:
// its answer is the only way to learn the hold to give back. One still
// unanswered then is abandoned; a hold it did take ends by itself when
// its lease runs out.
//
// Its error is what the screens had no chance to say: a release that
// didn't reach the server (an *attach.ReleaseError), its own or that of
// an attach a signal ended. That hold stays until its lease runs out.
func (a *App) Close() error {
	a.closeConversation()
	done := make(chan struct{})
	go func() {
		a.work.Wait()
		close(done)
	}()
	wait := time.NewTimer(closeWait)
	defer wait.Stop()
	select {
	case <-done:
	case <-wait.C:
		if st := a.starting; st != nil {
			st.cancel()
		}
		<-done
	}
	if st := a.starting; st != nil {
		a.settle(st)
	}
	errs := []error{a.unshown.take()}
	if sess := a.held.take(); sess != nil {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		errs = append(errs, sess.Release(ctx))
	}
	// A tmux window that opened and that the list never got to show:
	// closed again, like an attach cancelled while it opened.
	if id := a.window.take(); id != "" {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		_ = a.closeWindow(ctx, id)
	}
	return errors.Join(errs...)
}

// attachExec is the attach routine as Bubble Tea runs it with the
// terminal released: takeover done, it runs the command, renews the hold
// and releases it (attach.Session.Run).
type attachExec struct {
	held    *held
	preface string

	in        io.Reader
	out, errw io.Writer
	// ran: Bubble Tea handed the terminal over and called Run.
	ran bool
	// done is what the screens are told when they resume.
	done attachedMsg
}

func (e *attachExec) SetStdin(r io.Reader)  { e.in = r }
func (e *attachExec) SetStdout(w io.Writer) { e.out = w }
func (e *attachExec) SetStderr(w io.Writer) { e.errw = w }

// Run attaches. Bubble Tea doesn't act on signals while it runs, so the
// attach watches them itself: one ends the command and releases the hold.
// Its error travels in done, never as Run's: the screens resume either
// way.
func (e *attachExec) Run() error {
	e.ran = true
	sess := e.held.take()
	if sess == nil {
		e.done = attachedMsg{err: errors.New("the attach was stopped before it started")}
		return nil
	}
	ctx, signalled, stop := attach.Signals(context.Background())
	defer stop()
	if e.preface != "" {
		fmt.Fprintln(e.errw, e.preface)
	}
	res, err := sess.Run(ctx, attach.Terminal{In: e.in, Out: e.out, Err: e.errw,
		Offer: func(ctx context.Context, _ attach.Attempt, next attach.Command) bool {
			return attach.Ask(ctx, e.in, e.errw, fmt.Sprintf("Try %s (%s)?", next.Via, next), true)
		}})
	e.done = attachedMsg{res: res, err: err, sig: signalled()}
	return nil
}

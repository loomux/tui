package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

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
	// that the window's command needs too: a tmux window starts from the
	// tmux server's environment, not from this program's.
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
	task  string // its task, once known
	force bool   // take the task from the session that holds it
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

// dialog is text in the list's place: what an attach has to say that a
// footer line can't hold. With yes it is a question: the yes key answers
// it, any other key is a no. Without, any key closes it.
type dialog struct {
	lines []string
	yes   func() tea.Cmd
	no    func()
}

type (
	// taskMsg is the conversation's task, found.
	taskMsg struct {
		st   *starting
		task client.Task
		ok   bool
		err  error
	}
	// windowMsg is tmux's answer to the new window.
	windowMsg struct {
		st  *starting
		err error
	}
	// takenMsg is the takeover's answer. The session is in App.held.
	takenMsg struct {
		st  *starting
		err error
	}
	// attachedMsg is the attach, over: the terminal is the screens' again.
	attachedMsg struct {
		res attach.Result
		err error
		// sig is the signal that ended it, if one did.
		sig os.Signal
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
	st := &starting{ctx: ctx, cancel: cancel, conv: it.ConversationID, name: it.workspace}
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
	at := a.attach
	switch {
	case at.Mode == AttachInline || (at.Mode != AttachWindow && !at.InTmux):
	case !at.InTmux:
		st.preface = "not inside tmux, so there is no window to open: attaching here"
	case len(at.Window) == 0:
		st.preface = "no command for a tmux window: attaching here"
	default:
		return a.openWindow(st)
	}
	return a.take(st)
}

// openWindow opens the attach's own tmux window.
func (a *App) openWindow(st *starting) tea.Cmd {
	name := st.name
	argv := append(append([]string(nil), a.attach.Window...), "--task", st.task)
	env, open := a.attach.WindowEnv, a.newWindow
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(st.ctx, requestTimeout)
		defer cancel()
		return windowMsg{st, open(ctx, name, env, argv)}
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
		return nil
	case msg.err != nil:
		// Not the end of the attach: here, then.
		st.preface = "couldn't open a tmux window (" + clean(msg.err.Error()) + "): attaching here"
		return a.take(st)
	}
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
// added to its environment.
func tmuxNewWindow(ctx context.Context, name string, env, argv []string) error {
	args := []string{"new-window", "-n", windowName(name)}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	args = append(append(args, "--"), argv...)
	out, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput()
	if err != nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n"); line != "" {
			return fmt.Errorf("tmux new-window: %s", line)
		}
		return fmt.Errorf("tmux new-window: %w", err)
	}
	return nil
}

// take takes the hold. The session waits in a.held for gotTaken, where
// Close finds it if the screens end first.
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
		return takenMsg{st, err}
	}
}

func (a *App) gotTaken(msg takenMsg) tea.Cmd {
	st := msg.st
	if st != a.starting {
		return nil
	}
	if st.cancelled {
		a.settle(st)
		return a.giveBack()
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
		return e.done
	})
}

// giveBack releases a hold taken for an attach that isn't happening.
func (a *App) giveBack() tea.Cmd {
	sess := a.held.take()
	if sess == nil {
		return nil
	}
	a.work.Add(1)
	return func() tea.Msg {
		defer a.work.Done()
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		_ = sess.Release(ctx) // unreleased, it expires by itself
		return nil
	}
}

// askHeld puts the question about a task another session holds: taking
// it detaches that client, so it is the person's call.
func (a *App) askHeld(st *starting, hold *client.Hold) {
	who := attach.HeldLine(hold)
	a.dialog = &dialog{
		lines: []string{
			strings.ToUpper(who[:1]) + who[1:] + ".",
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
// as text where the person may want to run one by hand, else a line.
func (a *App) showAttachError(err error) {
	var (
		old  *attach.UnsupportedError
		none *attach.NoCommandError
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
		for _, s := range none.Skipped {
			lines = append(lines, "  "+s.Line, "    "+s.Reason, "")
		}
		a.dialog = &dialog{lines: lines}
	default:
		a.notice = "couldn't attach: " + clean(err.Error())
	}
}

func (a *App) gotAttached(msg attachedMsg) tea.Cmd {
	if st := a.starting; st != nil {
		a.settle(st)
	}
	// A signal while the attach had the terminal ended the attach, which
	// released the hold on its way out. An interrupt is the person's
	// ctrl+c at the command (ssh asking for a password): the list is
	// back. Any other told the client itself to stop.
	if msg.sig != nil && msg.sig != os.Interrupt {
		a.quitting = true
		return tea.Quit
	}
	switch {
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
		a.dialog = nil
		if d.yes == nil {
			return true, nil
		}
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
			return true, nil
		}
	}
	return false, nil
}

// dialogBody is the dialog's lines, wrapped to the width, in the list's
// place.
func (a *App) dialogBody() []string {
	height := a.bodyHeight()
	lines := []string{""}
	for _, l := range a.dialog.lines {
		indent := len(l) - len(strings.TrimLeft(l, " "))
		for _, w := range strings.Split(ansi.Wrap(l, max(8, a.width-2-indent), ""), "\n") {
			lines = append(lines, " "+strings.Repeat(" ", indent)+strings.TrimLeft(w, " "))
		}
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines[:height]
}

func (a *App) dialogFooter() string {
	if a.dialog.yes == nil {
		return faint.Render(fit(" any key closes this", a.width))
	}
	key := func(act keymap.Action) string {
		if k := a.keys.Keys(act); len(k) > 0 {
			return k[0]
		}
		return "?"
	}
	return faint.Render(fit(" "+bold.Render(key(keymap.Yes))+" take it over  "+bold.Render(key(keymap.No))+" leave it", a.width))
}

// closeWait is how long Close lets a takeover on its way finish before
// giving up on it.
const closeWait = 5 * time.Second

// Close ends what an attach left behind when the screens end, and gives
// back a hold taken and not yet attached with. Call it once the program
// has stopped.
//
// A takeover on its way to the server is waited for, closeWait at most:
// its answer is the only way to learn the hold to give back. One still
// unanswered then is abandoned; a hold it did take ends by itself when
// its lease runs out.
func (a *App) Close() {
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
	if sess := a.held.take(); sess != nil {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		_ = sess.Release(ctx) // unreleased, it expires by itself
	}
}

// attachExec is the attach routine as Bubble Tea runs it with the
// terminal released: takeover done, it runs the command, renews the hold
// and releases it (attach.Session.Run).
type attachExec struct {
	held    *held
	preface string

	in        io.Reader
	out, errw io.Writer
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

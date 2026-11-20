// Package attach attaches this terminal to a task's tmux session, always
// under a hold (the design in loomux/server,
// docs/design/tui-client-design.md, "Attach to a live agent and detach",
// "Server awareness: holds" and "Attach mechanics"):
//
//	takeover → choose a command → run it → heartbeat → release
//
// Take takes a drive hold on the task and chooses the commands this
// machine can run from the server's attach-info. The session it returns
// renews the hold from then on. Run hands the terminal to the first
// command, offers the next if that one fails fast, and releases the hold
// when the command ends, however it ends.
//
// What it never does:
//
//   - run a command without a hold. A server without the takeover route
//     gets its command shown as text (UnsupportedError), not run.
//   - run anything through a shell. A command is the server's argv,
//     executed directly, or its line split into words (splitWords).
//   - send keys to the session. The person's own keyboard, through the
//     attach command, is the only input.
//   - keep the command running once the hold is known to be gone.
package attach

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

	"github.com/Loomux/tui/client"
)

// API is the part of the server's API the attach routine calls:
// *client.Client.
type API interface {
	AttachInfo(ctx context.Context, taskID string) (*client.AttachInfo, error)
	Takeover(ctx context.Context, taskID string, r client.TakeoverRequest) (*client.Takeover, error)
	Heartbeat(ctx context.Context, taskID, holdID string) (*client.Hold, error)
	Release(ctx context.Context, taskID, holdID string) (*client.Release, error)
}

// Options are Take's.
type Options struct {
	// Config is the config file's [attach] table.
	Config Config
	// Force takes the task from another session's hold, whose client the
	// server detaches first. Ask the person before setting it.
	Force bool
}

// The lease's defaults (the design's "Expiry, crashes and restarts"): a
// heartbeat every third of the lease, so two may be lost, and a failed
// one tried again every 5 s while the lease lasts.
const (
	defaultTTL = 45 * time.Second
	retryEvery = 5 * time.Second
	// failFastWindow: a command that exits non-zero within it didn't
	// attach (the design's "Choosing the command").
	failFastWindow = 2 * time.Second
)

// Session is a task held by this client, from Take until Run or Release
// ends it. One of those must be called: until then the session keeps
// renewing the hold, and automation stays out of the task's pane.
type Session struct {
	TaskID string
	// Hold is the drive hold as taken.
	Hold client.Hold
	// Info is the takeover's attach-info, and Plan the commands chosen
	// from it.
	Info client.AttachInfo
	Plan Plan

	api API

	// every is the heartbeat's interval, retry the wait after one that
	// failed while the lease lasts, and beatTimeout one heartbeat's limit.
	every, retry, beatTimeout time.Duration
	// ttl is the lease's length.
	ttl time.Duration
	// failFast is the window in which a non-zero exit means the command
	// didn't attach.
	failFast time.Duration
	// killWait is how long a command gets to end after its hangup, before
	// it is killed.
	killWait time.Duration
	// A release is tried releaseTries times, releaseTimeout each,
	// releaseWait (doubling) apart.
	releaseTries   int
	releaseTimeout time.Duration
	releaseWait    time.Duration

	beat *heartbeat

	releaseOnce sync.Once
	released    *client.Release
	releaseErr  error
}

// Take takes a drive hold on the task and chooses how to attach to it.
// The session it returns is already renewing the hold: call Run, or
// Release.
//
// Before taking anything it asks the server for the task's attach-info
// and checks that this machine can run one of its commands, so a running
// turn isn't ended (a takeover ends it) for an attach that can't happen.
//
// Its errors: the API's own (*client.Error: client.CodeTaskHeld with the
// holder's hold, to ask about Force; client.CodeTaskInactive;
// client.CodeDetachFailed; a 404 for an unknown task), a
// *NoCommandError, and an *UnsupportedError from a server without the
// takeover route. In every case nothing is held when it returns one.
func Take(ctx context.Context, api API, taskID string, o Options) (*Session, error) {
	return take(ctx, api, taskID, o, nil)
}

// take is Take; tune, a test's, adjusts the session's timers before its
// heartbeat starts.
func take(ctx context.Context, api API, taskID string, o Options, tune func(*Session)) (*Session, error) {
	ch := newChooser()
	info, err := api.AttachInfo(ctx, taskID)
	if err != nil {
		return nil, err
	}
	plan := ch.plan(ctx, info, o.Config)
	if len(plan.Commands) == 0 {
		return nil, &NoCommandError{Info: *info, Skipped: plan.Skipped}
	}

	tk, err := api.Takeover(ctx, taskID, client.TakeoverRequest{Mode: client.ModeDrive, Force: o.Force})
	if client.Is(err, client.CodeNotFound) {
		// Attach-info knew the task, so what the server doesn't have is
		// the route: it is from before holds (the design's "Versions and
		// compatibility").
		return nil, &UnsupportedError{Info: *info, Plan: plan}
	}
	if err != nil {
		return nil, err
	}
	if tk.Hold == nil || tk.Hold.ID == "" {
		return nil, errors.New("the server answered the takeover without a hold: nothing was attached")
	}

	s := &Session{
		TaskID: taskID, Hold: *tk.Hold, Info: tk.Attach, api: api,
		ttl:      time.Duration(tk.Hold.TTLSeconds) * time.Second,
		retry:    retryEvery,
		failFast: failFastWindow, killWait: 3 * time.Second,
		releaseTries: 3, releaseTimeout: 5 * time.Second, releaseWait: 300 * time.Millisecond,
	}
	if s.ttl <= 0 {
		s.ttl = defaultTTL
	}
	s.every = s.ttl / 3
	s.beatTimeout = min(s.every, 10*time.Second)
	if tune != nil {
		tune(s)
	}

	// A drive was asked for. Whatever else the server granted isn't what
	// the person is about to do in the pane: give it back.
	if m := tk.Hold.Mode; m != "" && m != client.ModeDrive {
		err := fmt.Errorf("the server answered a request to drive with a %s hold: it was given back and nothing was attached", clean(m))
		return nil, errors.Join(err, s.Release(ctx))
	}
	// The commands to run are the ones the hold came with.
	s.Plan = ch.plan(ctx, &s.Info, o.Config)
	if len(s.Plan.Commands) == 0 {
		return nil, errors.Join(&NoCommandError{Info: s.Info, Skipped: s.Plan.Skipped}, s.Release(ctx))
	}
	s.startHeartbeat()
	return s, nil
}

// Terminal is what Run hands to the attach command.
type Terminal struct {
	// In, Out and Err are the command's standard streams: the terminal's
	// own files for a real attach. Run's own lines go to Err.
	In       io.Reader
	Out, Err io.Writer
	// Offer asks the person whether to try next after failed failed fast.
	// It is called with the hold still held. Without one, the next
	// command is tried.
	Offer func(ctx context.Context, failed Attempt, next Command) bool
}

func (t Terminal) say(format string, args ...any) {
	if t.Err != nil {
		fmt.Fprintf(t.Err, format+"\n", args...)
	}
}

// Attempt is one command run and how it ended.
type Attempt struct {
	Command Command
	// Started is false when the command couldn't be started at all.
	Started bool
	// Err is why it couldn't start, or how it ended: nil for exit
	// status 0, else an *exec.ExitError.
	Err      error
	Duration time.Duration
}

// String is the attempt's failure in words: "kubectl: exit status 1".
func (a Attempt) String() string {
	prog := clean(a.Command.Argv[0])
	switch {
	case !a.Started:
		return fmt.Sprintf("%s couldn't be started: %v", prog, a.Err)
	case a.Err != nil:
		return fmt.Sprintf("%s: %v", prog, a.Err)
	}
	return prog + ": exit status 0"
}

// Result is what Run did.
type Result struct {
	// Attempts are the commands run, in order.
	Attempts []Attempt
	// Attached reports whether a command got as far as attaching: it ran
	// without failing fast.
	Attached bool
	// Status is the task's status after the release, "" if unknown.
	Status string
}

// Run attaches: it runs the plan's first command on the terminal, and,
// when a command fails fast (it can't be started, exits non-zero within
// two seconds, or is ssh exiting 255), offers the next. When the command
// ends it releases the hold. It returns once the hold is released or
// known to be gone, with nil only if the person was attached and the
// hold released as their own.
//
// The command is ended, with a hangup and then a kill, when ctx is
// cancelled (give it a context a signal cancels) and when a heartbeat
// learns that the hold is gone: taken back or over from another client,
// expired, or the session signed out. The hold is released on every path
// but those where the server already ended it.
//
// Its errors: a *FailedError (no command attached), an *EndedError (the
// hold ended some other way than this release), a *ReleaseError (the
// release didn't reach the server), the heartbeat's *client.Error (a 401:
// signed out), and ctx's.
func (s *Session) Run(ctx context.Context, t Terminal) (Result, error) {
	var res Result
	commands := s.Plan.Commands
	for i, c := range commands {
		t.say("%s", s.attachingLine(c))
		a, how := s.attempt(ctx, c, t)
		res.Attempts = append(res.Attempts, a)
		if how != endedItself {
			res.Attached = a.Started
			break
		}
		if !s.failedFast(a) {
			res.Attached = true
			if a.Err != nil {
				t.say("%s", a)
			}
			break
		}
		t.say("%s", a)
		if i+1 == len(commands) || !s.offer(ctx, t, a, commands[i+1]) {
			break
		}
	}

	rel, relErr := s.release(ctx)
	if rel != nil {
		res.Status = rel.Status
	}
	switch gone := s.beat.goneErr(); {
	case gone != nil:
		return res, goneError(gone)
	case ctx.Err() != nil:
		return res, errors.Join(ctx.Err(), relErr)
	case relErr != nil:
		return res, relErr
	case rel.Hold != nil && rel.Hold.EndedReason != "" && rel.Hold.EndedReason != endedReleased:
		// The server had ended the hold before this release: say how.
		return res, endedError(rel.Hold, rel.Hold.EndedReason)
	case !res.Attached:
		return res, &FailedError{Attempts: res.Attempts}
	}
	return res, nil
}

// Release gives the hold back without attaching, and ends the heartbeat.
// It is safe to call again, and after Run.
func (s *Session) Release(ctx context.Context) error {
	_, err := s.release(ctx)
	return err
}

// attachingLine is the line each run prints first.
func (s *Session) attachingLine(c Command) string {
	name := clean(s.Info.Target.Name)
	if name == "" {
		name = "the task"
	}
	detach := "detach with <prefix> d (C-b d by default)"
	if os.Getenv("TMUX") != "" {
		// Inside the person's own tmux its prefix goes to that tmux:
		// pressed twice, it reaches the agent's.
		detach = "detach with <prefix> <prefix> d from inside your own tmux (C-b C-b d by default)"
	}
	return fmt.Sprintf("attaching to %s via %s, %s", name, c.Via, detach)
}

// how an attempt ended.
type ending int

const (
	endedItself ending = iota // the command exited, or couldn't start
	endedByStop               // ctx was cancelled
	endedByHold               // the hold is gone
)

// attempt runs one command on the terminal until it ends, ctx is
// cancelled or the hold is gone, and leaves the terminal as it found it.
func (s *Session) attempt(ctx context.Context, c Command, t Terminal) (Attempt, ending) {
	a := Attempt{Command: c}
	restore := saveTerminal(t)
	start := time.Now()
	p, err := startProcess(c, t)
	if err != nil {
		a.Err = err
		restore(false)
		return a, endedItself
	}
	a.Started = true
	how := endedItself
	select {
	case <-p.done:
	case <-s.beat.gone:
		how = endedByHold
		p.end(s.killWait)
	case <-ctx.Done():
		how = endedByStop
		p.end(s.killWait)
	}
	a.Err, a.Duration = p.err, time.Since(start)
	// A command that didn't end cleanly may have left the terminal's
	// modes as the remote tmux set them.
	restore(a.Err != nil || how != endedItself)
	return a, how
}

// failedFast reports whether the command didn't attach (the design's
// "Choosing the command"): it couldn't start, it exited non-zero within
// the window, or it is ssh exiting 255, its own failure whenever it
// comes.
func (s *Session) failedFast(a Attempt) bool {
	if !a.Started {
		return true
	}
	if a.Err == nil {
		return false
	}
	var exit *exec.ExitError
	if a.Command.Argv[0] == "ssh" && errors.As(a.Err, &exit) && exit.ExitCode() == 255 {
		return true
	}
	return a.Duration < s.failFast
}

// offer asks whether to try next; ctx cancelled or the hold gone
// meanwhile is a no.
func (s *Session) offer(ctx context.Context, t Terminal, failed Attempt, next Command) bool {
	if ctx.Err() != nil || s.beat.goneErr() != nil {
		return false
	}
	if t.Offer == nil {
		return true
	}
	answer := make(chan bool, 1)
	go func() { answer <- t.Offer(ctx, failed, next) }()
	select {
	case yes := <-answer:
		return yes && ctx.Err() == nil && s.beat.goneErr() == nil
	case <-ctx.Done():
	case <-s.beat.gone:
	}
	return false
}

// release ends the heartbeat and releases the hold, once. The hold is
// released even when ctx is cancelled: a signal is when it matters most.
func (s *Session) release(ctx context.Context) (*client.Release, error) {
	s.releaseOnce.Do(func() {
		s.beat.stop()
		if s.beat.goneErr() != nil {
			return // the server ended it: there is nothing to release
		}
		ctx := context.WithoutCancel(ctx)
		for try := 1; ; try++ {
			rctx, cancel := context.WithTimeout(ctx, s.releaseTimeout)
			rel, err := s.api.Release(rctx, s.TaskID, s.Hold.ID)
			cancel()
			if err == nil {
				s.released = rel
				return
			}
			if try >= s.releaseTries || !transient(err) {
				s.releaseErr = &ReleaseError{Err: err, Expires: s.beat.expiry(s.Hold.ExpiresAt)}
				return
			}
			time.Sleep(s.releaseWait << (try - 1))
		}
	})
	return s.released, s.releaseErr
}

// transient reports whether a request that failed with err may work if
// sent again: it didn't reach the server, or reached one that couldn't
// take it.
func transient(err error) bool {
	var e *client.Error
	if errors.As(err, &e) {
		return e.Status >= 500 || e.Status == 429
	}
	var ie *client.InterceptedError
	return !errors.As(err, &ie)
}

// The reasons a hold ends (the hold's ended_reason).
const (
	endedReleased       = "released"
	endedExpired        = "expired"
	endedTakenBack      = "taken_back"
	endedTakenOver      = "taken_over"
	endedSessionRevoked = "session_revoked"
	endedTaskEnded      = "task_ended"
)

// EndedError says the hold ended some other way than this client's
// release: the server (or another client through it) ended it, and
// detached this terminal.
type EndedError struct {
	// Reason is the hold's ended_reason: expired, taken_back, taken_over,
	// session_revoked, task_ended, or one a later server adds.
	Reason string
	// At is when, zero if the server didn't say.
	At time.Time
}

func (e *EndedError) Error() string {
	at := ""
	if !e.At.IsZero() {
		at = " at " + e.At.Local().Format("15:04")
	}
	switch e.Reason {
	case endedExpired:
		return "your hold expired" + at + "; the session was detached"
	case endedTakenBack:
		return "the task was taken back from another client" + at + "; the session was detached"
	case endedTakenOver:
		return "another session took the task over" + at + "; the session was detached"
	case endedSessionRevoked:
		return "this session was signed out" + at + "; the session was detached. Sign in again with loomux login"
	case endedTaskEnded:
		return "the task ended" + at
	}
	return "your hold ended" + at + " (" + clean(e.Reason) + ")"
}

func endedError(hold *client.Hold, reason string) *EndedError {
	e := &EndedError{Reason: reason}
	if hold != nil {
		if e.Reason == "" {
			e.Reason = hold.EndedReason
		}
		if hold.EndedAt != nil {
			e.At = *hold.EndedAt
		}
	}
	return e
}

// goneError is Run's error for a heartbeat answer that ended the attach.
func goneError(err error) error {
	var e *client.Error
	if errors.As(err, &e) {
		switch {
		case e.Code == client.CodeHoldEnded:
			return endedError(e.Hold, e.EndedReason)
		case client.Unauthorized(err):
			return fmt.Errorf("this session was signed out (%w); the attach was ended. Sign in again with loomux login", err)
		}
	}
	return fmt.Errorf("the hold can't be renewed (%w); the attach was ended", err)
}

// ReleaseError says the release didn't reach the server. The hold is
// still held, and ends by itself when its lease runs out.
type ReleaseError struct {
	Err error
	// Expires is when the lease runs out if nothing renews it.
	Expires time.Time
}

func (e *ReleaseError) Error() string {
	return fmt.Sprintf("couldn't release the hold (%v): it expires by itself at %s, and the server then hands the task back",
		e.Err, e.Expires.Local().Format("15:04:05"))
}

func (e *ReleaseError) Unwrap() error { return e.Err }

// FailedError says no command attached: each failed fast, or the next
// one was declined. The hold was released.
type FailedError struct {
	Attempts []Attempt
}

func (e *FailedError) Error() string {
	why := make([]string, len(e.Attempts))
	for i, a := range e.Attempts {
		why[i] = a.String()
	}
	return "couldn't attach (" + strings.Join(why, "; ") + "); the hold was released"
}

// NoCommandError says this machine can run none of the server's attach
// commands. Nothing is held.
type NoCommandError struct {
	Info client.AttachInfo
	// Skipped are the commands and why each can't be used; empty when the
	// server gave none.
	Skipped []Skipped
}

func (e *NoCommandError) Error() string {
	if len(e.Skipped) == 0 {
		return "the server gave no command to attach with"
	}
	why := make([]string, len(e.Skipped))
	for i, s := range e.Skipped {
		why[i] = s.Via + ": " + s.Reason
	}
	return "no attach command can be run from this machine (" + strings.Join(why, "; ") + ")"
}

// UnsupportedError says the server has no takeover route: it is from
// before holds, and can't attach yet. Plan has its commands, to show as
// text: they are never run without a hold.
type UnsupportedError struct {
	Info client.AttachInfo
	Plan Plan
}

func (e *UnsupportedError) Error() string {
	return "this server can't attach yet (it has no takeover): upgrade it"
}

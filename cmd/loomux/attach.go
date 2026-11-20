package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/term"

	"github.com/Loomux/tui/attach"
	"github.com/Loomux/tui/auth"
	"github.com/Loomux/tui/client"
	"github.com/Loomux/tui/config"
)

// What an attach reads the person's answers from, and whether it has a
// terminal to hand over; tests replace them.
var (
	stdin      io.Reader = os.Stdin
	isTerminal           = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	}
)

// attachMain is `loomux attach`: takeover → the attach command on this
// terminal → heartbeat → release, with no screens (package attach, and
// the design's "Suspending and resuming the TUI"). Its exit status is 0
// when the person was attached and the hold released, 2 for a usage
// error, 128 plus the signal's number when a signal ended it, and 1
// otherwise.
func attachMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("loomux attach", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	profile := fs.String("profile", "", "")
	task := fs.String("task", "", "")
	force := fs.Bool("force", false, "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprint(stderr, usage)
		return 2
	}
	// One id: --task's, or the argument's.
	id := fs.Arg(0)
	if (*task == "") == (fs.NArg() == 0) || fs.NArg() > 1 || (*task == "" && id == "") {
		fmt.Fprint(stderr, usage)
		return 2
	}
	// Before anything is taken: a takeover ends a running turn.
	if !isTerminal() {
		fmt.Fprintln(stderr, "loomux: attach needs a terminal: its standard input and output aren't one")
		return 1
	}

	ctx, signalled, stop := withSignals(context.Background())
	defer stop()
	err := runAttach(ctx, *profile, *task, id, *force, stdout, stderr)
	if sig := signalled(); sig != nil {
		// The hold was released on the way out, unless that failed.
		var rel *attach.ReleaseError
		if errors.As(err, &rel) {
			fmt.Fprintf(stderr, "loomux: %v\n", rel)
		}
		if n, ok := sig.(syscall.Signal); ok {
			return 128 + int(n)
		}
		return 1
	}
	if err != nil {
		explain(stderr, err)
		return 1
	}
	return 0
}

func runAttach(ctx context.Context, profile, taskID, id string, force bool, stdout, stderr io.Writer) error {
	c, cfg, err := signIn(profile, stderr)
	if err != nil {
		return err
	}
	if taskID == "" {
		if taskID, err = taskFor(ctx, c, id); err != nil {
			return err
		}
	}

	opts := attach.Options{
		Config: attach.Config{Prefer: cfg.Attach.Prefer, KubectlContext: cfg.Attach.Kubectl.Context},
		Force:  force,
	}
	sess, err := attach.Take(ctx, c, taskID, opts)
	var held *client.Error
	if errors.As(err, &held) && held.Code == client.CodeTaskHeld && !opts.Force {
		// Another session has the task: taking it detaches that client,
		// so it is the person's call.
		fmt.Fprintln(stderr, heldLine(held.Hold))
		if !ask(stdin, stderr, "Take it over? That client is detached.", false) {
			return errors.New("left with its holder: nothing was taken")
		}
		opts.Force = true
		sess, err = attach.Take(ctx, c, taskID, opts)
	}
	if err != nil {
		return err
	}

	res, err := sess.Run(ctx, attach.Terminal{In: stdin, Out: stdout, Err: stderr,
		Offer: func(_ context.Context, _ attach.Attempt, next attach.Command) bool {
			return ask(stdin, stderr, fmt.Sprintf("Try %s (%s)?", next.Via, next), true)
		}})
	if err != nil {
		return err
	}
	if res.Status != "" {
		fmt.Fprintf(stderr, "detached: the hold is released and the task is %s\n", attach.Clean(res.Status))
	} else {
		fmt.Fprintln(stderr, "detached: the hold is released")
	}
	return nil
}

// signIn returns a client of the profile's server, signed in with the
// profile's stored token, and the config.
func signIn(profile string, stderr io.Writer) (*client.Client, *config.Config, error) {
	path, err := config.Path()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	p, err := cfg.Select(profile, os.Getenv)
	if err != nil {
		return nil, nil, err
	}
	store, err := auth.Open(cfg)
	if err != nil {
		return nil, nil, err
	}
	account := auth.AccountFor(p)
	if w := auth.Warning(store, account); w != "" {
		fmt.Fprintln(stderr, w)
	}
	token, err := store.Get(account)
	if errors.Is(err, auth.ErrNotFound) {
		return nil, nil, fmt.Errorf("not signed in to profile %q (%s): sign in with loomux login", p.Name, p.Server)
	}
	if err != nil {
		return nil, nil, err
	}
	c, err := client.New(p.Server, token)
	if err != nil {
		return nil, nil, err
	}
	return c, cfg, nil
}

// taskFor is the task `loomux attach <id>` means: the open agent task of
// the conversation with that id, or, when no conversation has it, the
// task with that id.
func taskFor(ctx context.Context, c *client.Client, id string) (string, error) {
	conv, err := c.Conversation(ctx, id, client.Page{Limit: 1})
	if client.Is(err, client.CodeNotFound) {
		return id, nil
	}
	if err != nil {
		return "", err
	}
	task, ok := attach.TaskOf(conv.Tasks)
	if !ok {
		return "", errors.New("that conversation has no agent to attach to: none of its agents is open")
	}
	return task.ID, nil
}

// heldLine names who holds a task.
func heldLine(h *client.Hold) string {
	if h == nil {
		return "the task is held by another client"
	}
	from := "another client"
	switch h.ClientKind {
	case "tui":
		from = "the terminal"
	case "web":
		from = "the web"
	case "app":
		from = "the app"
	}
	line := "the task is being driven from " + from
	if label := attach.Clean(h.ClientLabel); label != "" {
		line += " on " + label
	}
	if !h.AcquiredAt.IsZero() {
		line += " since " + h.AcquiredAt.Local().Format("15:04")
	}
	return line
}

// explain prints why an attach failed, with the server's commands as
// text where the person may want to run one by hand.
func explain(stderr io.Writer, err error) {
	var (
		old  *attach.UnsupportedError
		none *attach.NoCommandError
	)
	switch {
	case errors.As(err, &old):
		fmt.Fprintf(stderr, "loomux: %v\n", err)
		fmt.Fprintln(stderr, "Until then, to attach by hand (Loomux won't know someone is in the pane, and may type into it):")
		for _, c := range old.Plan.Commands {
			fmt.Fprintf(stderr, "  %s\n", c)
		}
	case errors.As(err, &none):
		fmt.Fprintf(stderr, "loomux: %v\n", err)
		for _, s := range none.Skipped {
			fmt.Fprintf(stderr, "  %s\n", s.Line)
		}
	case client.Unauthorized(err) && !strings.Contains(err.Error(), "loomux login"):
		fmt.Fprintf(stderr, "loomux: the server refused the stored token (%v): sign in again with loomux login\n", err)
	default:
		fmt.Fprintf(stderr, "loomux: %v\n", err)
	}
}

// ask puts a yes/no question and reads one line of answer: y or yes,
// n or no, or nothing for def. Anything else is a no, and so is no
// answer at all (the input is gone). It reads its line and nothing after
// it: the rest is the next command's.
func ask(in io.Reader, out io.Writer, question string, def bool) bool {
	hint := " [y/N] "
	if def {
		hint = " [Y/n] "
	}
	fmt.Fprint(out, question+hint)
	var line []byte
	for b := make([]byte, 1); ; {
		n, err := in.Read(b)
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if len(line) > 256 || (err != nil && len(line) == 0) {
			return false
		}
		if err != nil {
			break
		}
	}
	switch strings.ToLower(strings.TrimSpace(string(line))) {
	case "":
		return def
	case "y", "yes":
		return true
	}
	return false
}

// withSignals returns a context that the first SIGINT, SIGTERM or SIGHUP
// cancels, so that the attach ends its command and releases the hold
// before the client exits, and what reports that signal. A second signal
// isn't caught: it ends the client at once, and the hold then expires by
// itself.
func withSignals(parent context.Context) (ctx context.Context, signalled func() os.Signal, stop func()) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	var (
		mu  sync.Mutex
		got os.Signal
	)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-ch:
			mu.Lock()
			got = sig
			mu.Unlock()
			signal.Stop(ch)
			cancel()
		case <-done:
		}
	}()
	signalled = func() os.Signal {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
	return ctx, signalled, func() {
		signal.Stop(ch)
		close(done)
		cancel()
	}
}

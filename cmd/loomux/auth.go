package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Loomux/tui/auth"
	"github.com/Loomux/tui/config"
)

// What login and logout take from the machine; tests replace them.
var (
	// signalContext is cancelled by ctrl+c or SIGTERM. After the first
	// one the default comes back, so a second ctrl+c ends the program at
	// once.
	signalContext = func() (context.Context, context.CancelFunc) {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		go func() {
			<-ctx.Done()
			stop()
		}()
		return ctx, stop
	}
	// openBrowser opens the link in the user's browser and reports
	// whether it started one.
	openBrowser = startBrowser
	// hostname names this machine: the session's label.
	hostname = os.Hostname
	// linkInterval and linkTimeout are the link flow's wait: the
	// design's 2 s and 10 minutes when zero.
	linkInterval, linkTimeout time.Duration
)

// openProfile loads the config, selects the run's profile (flagProfile
// is --profile) and opens its token store.
func openProfile(flagProfile string) (config.Profile, auth.Store, error) {
	path, err := config.Path()
	if err != nil {
		return config.Profile{}, nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Profile{}, nil, err
	}
	p, err := cfg.Select(flagProfile, os.Getenv)
	if err != nil {
		return config.Profile{}, nil, err
	}
	store, err := auth.Open(cfg)
	if err != nil {
		return config.Profile{}, nil, err
	}
	return p, store, nil
}

// login is `loomux login`: the link flow (the design's "The link flow").
// It prints the link and the code, opens the link in a browser when it
// can, and waits for the approval. The token is never printed.
func login(profile string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("loomux login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&profile, "profile", profile, "")
	noBrowser := fs.Bool("no-browser", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	p, store, err := openProfile(profile)
	if err != nil {
		fmt.Fprintf(stderr, "loomux: login: %v\n", err)
		return 1
	}
	account := auth.AccountFor(p)
	if w := auth.Warning(store, account); w != "" {
		fmt.Fprintln(stderr, w)
	}
	host, _ := hostname() // no host name: auth's label for that

	ctx, stop := signalContext()
	defer stop()
	shown := false
	l := auth.Login{Account: account, Store: store, Label: host, Interval: linkInterval, Timeout: linkTimeout}
	l.Show = func(link auth.Link) {
		shown = true
		fmt.Fprintf(stdout, "Signing in to profile %q (%s) as %q.\n\n", p.Name, p.Server, link.Label)
		fmt.Fprintf(stdout, "Open this link in a browser that is signed in to Loomux:\n\n  %s\n\n", link.URL)
		fmt.Fprintf(stdout, "Its page must show this code. Approve only if it does:\n\n  %s\n\n", link.Code)
		if !*noBrowser && openBrowser(link.URL) {
			fmt.Fprintln(stdout, "The link is opening in your browser.")
		}
		fmt.Fprintf(stdout, "Waiting for the approval, for up to %s (ctrl+c cancels)…\n", waitWords(linkTimeout))
	}
	res, err := l.Run(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "loomux: login: %v\n", err)
		if shown && (errors.Is(err, context.Canceled) || errors.Is(err, auth.ErrLinkTimeout)) {
			// The link still works on the page (the server can't tell
			// the terminal stopped waiting), and its token is gone.
			fmt.Fprintln(stderr, "Nothing was stored, and this terminal isn't signed in. If the link is approved after this, choose Undo approval on its page.")
		}
		return 1
	}
	if res.Already {
		fmt.Fprintf(stdout, "Already signed in to profile %q (%s)%s. To sign in again, run `loomux logout` first.\n", p.Name, p.Server, as(res.Label))
		return 0
	}
	fmt.Fprintf(stdout, "Signed in to profile %q (%s)%s.\n", p.Name, p.Server, as(res.Label))
	return 0
}

// logout is `loomux logout`: the session is ended on the server, then
// the token is deleted from the token store, also when the server
// couldn't be asked. That case is said, and exits 1: the session may
// still exist.
func logout(profile string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("loomux logout", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&profile, "profile", profile, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	p, store, err := openProfile(profile)
	if err != nil {
		fmt.Fprintf(stderr, "loomux: logout: %v\n", err)
		return 1
	}
	ctx, stop := signalContext()
	defer stop()
	res, err := auth.Logout(ctx, store, auth.AccountFor(p))
	switch {
	case !res.Stored && err != nil: // the store can't be used at all
		fmt.Fprintf(stderr, "loomux: logout: %v\n", err)
		return 1
	case !res.Stored:
		fmt.Fprintf(stdout, "Not signed in to profile %q (%s): nothing to do.\n", p.Name, p.Server)
		return 0
	case res.NotRevoked == nil && err == nil:
		fmt.Fprintf(stdout, "Signed out of profile %q (%s).\n", p.Name, p.Server)
		return 0
	}
	if res.NotRevoked != nil {
		fmt.Fprintf(stderr, "loomux: logout: the session couldn't be ended on the server (%s): %v\n", p.Server, res.NotRevoked)
	}
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "loomux: logout: the token couldn't be deleted from the token store: %v\n", err)
	case res.NotRevoked != nil:
		fmt.Fprintln(stderr, "The token was deleted from this machine, so this terminal is signed out. The session may still exist on the server: revoke it in the web's Settings → Devices, or it expires on its own.")
	}
	return 1
}

// as names a session's label in a message, or nothing for a session
// without one. The label is the server's: only its printable characters
// reach the terminal.
func as(label string) string {
	if label == "" {
		return ""
	}
	return fmt.Sprintf(" as %q", auth.LinkLabel(label))
}

// waitWords says how long the link flow waits (zero: the default), in
// the words the /link page uses for it: "10 minutes".
func waitWords(d time.Duration) string {
	if d <= 0 {
		d = auth.DefaultLinkTimeout
	}
	if d < time.Minute || d%time.Minute != 0 {
		return d.String()
	}
	if d == time.Minute {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", d/time.Minute)
}

package attach

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Signals returns a context that the first SIGINT, SIGTERM or SIGHUP
// cancels, so that the attach ends its command and releases the hold
// before the client exits, and what reports that signal. A second signal
// isn't caught: it ends the client at once, and the hold then expires by
// itself.
func Signals(parent context.Context) (ctx context.Context, signalled func() os.Signal, stop func()) {
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

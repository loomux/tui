//go:build unix

package attach

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// The first signal cancels the context and is reported; stop ends the
// watch.
func TestSignals(t *testing.T) {
	ctx, signalled, stop := Signals(context.Background())
	defer stop()
	if signalled() != nil || ctx.Err() != nil {
		t.Fatal("signalled before any signal")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the signal didn't cancel the context")
	}
	if got := signalled(); got != syscall.SIGHUP {
		t.Errorf("signalled() = %v, want SIGHUP", got)
	}

	// Stopped without a signal: the context ends, and no signal is
	// reported.
	ctx, signalled, stop = Signals(context.Background())
	stop()
	if ctx.Err() == nil || signalled() != nil {
		t.Errorf("after stop: ctx %v, signalled %v", ctx.Err(), signalled())
	}
}

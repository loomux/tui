//go:build unix

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// Ctrl+c cancels the context login and logout run under, instead of
// ending the program with the link still waiting.
func TestSignalContextIsCancelledByCtrlC(t *testing.T) {
	ctx, stop := signalContext()
	defer stop()
	select {
	case <-ctx.Done():
		t.Fatal("cancelled before any signal")
	default:
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Minute):
		t.Fatal("ctrl+c didn't cancel the context")
	}
}

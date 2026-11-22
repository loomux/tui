//go:build unix

package attach

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// A question that ended with its context has let go of the terminal:
// what is typed afterwards is the next reader's (the screens', when they
// resume), every key of it.
func TestAskLeavesTheInputWhenCancelled(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	answered := make(chan bool, 1)
	go func() { answered <- Ask(ctx, r, io.Discard, "Go on?", true) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case got := <-answered:
		if got {
			t.Error("a cancelled question was a yes")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Ask didn't end when its context was cancelled")
	}

	if _, err := w.WriteString("y\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // a reader left behind would take it now
	if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 8)
	n, err := r.Read(got)
	if err != nil || string(got[:n]) != "y\n" {
		t.Errorf("the next reader got %q (%v), want every key typed after the question ended", got[:n], err)
	}
}

// A file is read a line at a time and no further, as any other input.
func TestAskReadsAFileOneLine(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := w.WriteString("yes\nrest"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if !Ask(context.Background(), r, io.Discard, "Go on?", false) {
		t.Error("yes wasn't a yes")
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != "rest" {
		t.Errorf("after the answer the input has %q left, want %q", rest, "rest")
	}
	// The input closed with nothing typed is no answer.
	r2, w2, _ := os.Pipe()
	w2.Close()
	defer r2.Close()
	if Ask(context.Background(), r2, io.Discard, "Go on?", true) {
		t.Error("a closed input was a yes")
	}
}

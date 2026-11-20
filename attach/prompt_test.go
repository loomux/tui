package attach

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Loomux/tui/client"
)

func TestAsk(t *testing.T) {
	for _, tc := range []struct {
		typed string
		def   bool
		want  bool
	}{
		{"y\n", false, true}, {"Y\n", false, true}, {"yes\n", false, true}, {" yes \r\n", false, true},
		{"n\n", true, false}, {"no\n", true, false}, {"N\n", true, false},
		{"\n", true, true}, {"\n", false, false},
		{"maybe\n", true, false}, {"maybe\n", false, false},
		// No answer at all (the terminal is gone) is never a yes.
		{"", true, false}, {"", false, false},
		{"y", false, true},
	} {
		var out bytes.Buffer
		if got := Ask(context.Background(), strings.NewReader(tc.typed), &out, "Go on?", tc.def); got != tc.want {
			t.Errorf("typed %q (default %v): %v, want %v", tc.typed, tc.def, got, tc.want)
		}
		want := "Go on? [y/N] "
		if tc.def {
			want = "Go on? [Y/n] "
		}
		if out.String() != want {
			t.Errorf("the question = %q, want %q", out.String(), want)
		}
	}
	// It reads its own line and no further: what follows is the next
	// program's.
	in := strings.NewReader("y\nrest")
	Ask(context.Background(), in, io.Discard, "Go on?", false)
	if rest, _ := io.ReadAll(in); string(rest) != "rest" {
		t.Errorf("after the answer the input has %q left, want %q", rest, "rest")
	}
}

// A question left unanswered ends when the context does (a signal): the
// client doesn't sit at a prompt it was told to leave.
func TestAskEndsWhenCancelled(t *testing.T) {
	never, w := io.Pipe() // an input nobody types into
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	answered := make(chan bool, 1)
	go func() { answered <- Ask(ctx, never, io.Discard, "Go on?", true) }()
	select {
	case got := <-answered:
		t.Fatalf("Ask answered %v with nothing typed", got)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case got := <-answered:
		if got {
			t.Error("a cancelled question was a yes")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Ask didn't end when its context was cancelled")
	}
}

func TestHeldLine(t *testing.T) {
	at := time.Date(2026, 11, 20, 14, 2, 0, 0, time.Local)
	for _, tc := range []struct {
		hold *client.Hold
		want string
	}{
		{&client.Hold{ClientKind: "tui", ClientLabel: "laptop", AcquiredAt: at}, "the task is being driven from the terminal on laptop since 14:02"},
		{&client.Hold{ClientKind: "web", ClientLabel: "office"}, "the task is being driven from the web on office"},
		{&client.Hold{ClientKind: "app"}, "the task is being driven from the app"},
		{&client.Hold{ClientKind: "later-kind", ClientLabel: "x\x1b[2Jy"}, "the task is being driven from another client on x[2Jy"},
		{nil, "the task is held by another client"},
	} {
		if got := HeldLine(tc.hold); got != tc.want {
			t.Errorf("HeldLine(%+v) = %q, want %q", tc.hold, got, tc.want)
		}
	}
}

func TestPause(t *testing.T) {
	// It waits for enter, and takes that line alone.
	in := strings.NewReader("\nrest")
	var out bytes.Buffer
	Pause(context.Background(), in, &out, "Press enter.")
	if rest, _ := io.ReadAll(in); string(rest) != "rest" || out.String() != "Press enter. " {
		t.Errorf("rest %q, shown %q", rest, out.String())
	}
	// It ends with its context, and with the input.
	never, w := io.Pipe()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Pause(ctx, never, io.Discard, "Press enter."); close(done) }()
	select {
	case <-done:
		t.Fatal("Pause didn't wait")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Pause didn't end with its context")
	}
	Pause(context.Background(), strings.NewReader(""), io.Discard, "Press enter.")
}

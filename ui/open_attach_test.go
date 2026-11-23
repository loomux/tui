//go:build unix

package ui

// Opening a conversation while an attach is starting.

import (
	"strings"
	"testing"

	"github.com/Loomux/tui/client"
)

// OpenStream is a closed stream, as fakeAPI's is.
func (c countedAPI) OpenStream(id string) *client.Stream { return closedStream(id) }

// An attach on its way is about to take the terminal or ask a question:
// enter opens nothing meanwhile, and the attach goes on.
func TestEnterWhileAttaching(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	w.send(keyPress("a")) // the answer waits in w.out
	w.send(keyPress("enter"))
	if f := w.footer(); !strings.Contains(f, "attaching…") {
		t.Errorf("footer = %q, want it to say the attach is on its way", f)
	}
	w.flush()
	if !w.ran("ran") {
		t.Error("the attach didn't go on")
	}
	if f := w.footer(); !strings.Contains(f, "the hold is released") {
		t.Errorf("footer = %q", f)
	}
}

// An attach cancelled while its takeover was on its way still gives the
// hold back when a conversation has been opened meanwhile.
func TestCancelledAttachGivesBackBehindTheConversation(t *testing.T) {
	w := newAttachWorld(t, "inline", false)
	w.ssh("touch " + w.file("ran"))
	w.send(keyPress("a"))
	w.feedOne() // the task: the takeover runs, its answer waits
	if h := w.srv.ActiveHold(w.task.ID); h == nil {
		t.Fatal("no hold after the takeover")
	}
	w.send(keyPress("esc"))
	w.send(keyPress("enter"))
	w.flush()
	if f := w.frame(); !strings.Contains(f, "loomux · test · apitest") || !strings.Contains(f, "on it") {
		t.Errorf("the conversation isn't shown:\n%s", f)
	}
	if h := w.srv.ActiveHold(w.task.ID); h != nil {
		t.Errorf("the hold wasn't given back: %+v", h)
	}
	if w.ran("ran") {
		t.Error("the command ran after the cancel")
	}
}

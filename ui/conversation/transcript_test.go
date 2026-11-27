package conversation

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Loomux/tui/client"
)

// turns is a task's transcript of two turns.
func turns() *client.Transcript {
	return &client.Transcript{TaskID: "t1", Turns: []client.Turn{
		{ID: "u1", UserMessage: "fix the flaky ConversationPage test", AgentMessage: "Fixed **both** races.",
			Pane: "$ go test ./...\nok  \tui\t0.3s\n\n\n", CreatedAt: at(2 * time.Minute)},
		{ID: "u2", UserMessage: "and the nightly?", AgentMessage: "Green.",
			Pane: "$ gh run list\n✓ nightly", CreatedAt: at(8 * time.Minute)},
	}}
}

// withTurns opens the fixture conversation with a task whose transcript
// is tr.
func withTurns(t *testing.T, tr *client.Transcript) *harness {
	t.Helper()
	h := opened(t, fixture())
	h.api.turns = func(string, client.Page) (*client.Transcript, error) { return tr, nil }
	return h
}

func TestTranscriptKeyShowsTheTasksTurns(t *testing.T) {
	h := withTurns(t, turns())
	h.key("p")

	if want := []turnsCall{{"t1", client.Page{}}}; !slices.Equal(h.api.turnsCalls, want) {
		t.Errorf("transcript fetches = %+v, want %+v", h.api.turnsCalls, want)
	}
	h.has("loomux · test · cc-hub · transcript of claude-code",
		"12:02", "fix the flaky ConversationPage test", "Fixed both races.", "$ go test ./...", "ok      ui    0.3s",
		"12:08", "and the nightly?", "Green.", "$ gh run list", "✓ nightly")
	h.lacks("**both**", "I fixed two things:", "p transcript")

	// These two turns are all of it: the top is the top.
	h.key("g", "k", "pgup")
	if len(h.api.turnsCalls) != 1 {
		t.Errorf("transcript fetches = %+v, want no page before the first turn", h.api.turnsCalls)
	}
}

func TestASignedOutTranscriptAsksForNoOlderTurns(t *testing.T) {
	h := opened(t, fixture())
	h.api.turns = func(string, client.Page) (*client.Transcript, error) {
		return &client.Transcript{Turns: manyTurns(21, 40), HasMore: true, NextBefore: "u21"}, nil
	}
	h.key("p")
	h.send(streamMsg{inst: h.m.inst, err: &client.Error{Status: http.StatusUnauthorized, Code: client.CodeUnauthorized}})
	h.key("g")

	h.has("turn 21", "loomux login")
	h.lacks("loading older turns…")
	if len(h.api.turnsCalls) != 1 {
		t.Errorf("signed out, it fetched %+v", h.api.turnsCalls[1:])
	}
}

// A transcript's fetch can answer after its transcript was left: its 401
// is still the token's, so the screen signs out all the same.
func TestTranscriptLeftBeforeIts401StillSignsOut(t *testing.T) {
	h := withTurns(t, turns())
	h.api.turns = func(string, client.Page) (*client.Transcript, error) {
		return nil, &client.Error{Status: http.StatusUnauthorized, Code: client.CodeUnauthorized}
	}
	h.hold()
	h.key("p")   // the fetch is in flight
	h.key("esc") // back to the conversation: the transcript is left
	if h.m.tr != nil {
		t.Fatal("the transcript is still open")
	}
	h.release() // it answers 401
	if len(h.out) != 1 {
		t.Fatalf("told the root %v, want SignedOutMsg", h.out)
	}
	if _, ok := h.out[0].(SignedOutMsg); !ok {
		t.Errorf("told the root %#v, want SignedOutMsg", h.out[0])
	}
	h.has("loomux login")
}

func TestBackOnTheTranscriptReturnsToTheConversation(t *testing.T) {
	h := withTurns(t, turns())
	h.key("p", "esc")

	h.has("I fixed two things:", "p transcript")
	h.lacks("$ go test")
	if len(h.out) != 0 || h.stream.closed {
		t.Errorf("leaving the transcript left the conversation: told the root %v, stream closed %v", h.out, h.stream.closed)
	}
	h.key("esc")
	if len(h.out) != 1 || h.out[0] != (BackMsg{}) {
		t.Errorf("back on the conversation told the root %v, want BackMsg", h.out)
	}
}

func TestQuitOnTheTranscriptQuits(t *testing.T) {
	h := withTurns(t, turns())
	h.key("p", "q")
	if len(h.out) != 1 {
		t.Fatalf("told the root %v, want tea.QuitMsg", h.out)
	}
	if _, ok := h.out[0].(tea.QuitMsg); !ok || !h.stream.closed {
		t.Errorf("told the root %#v (stream closed %v), want tea.QuitMsg and the stream closed", h.out[0], h.stream.closed)
	}
}

// The stream reports the conversation's most recently updated task, and
// that is the one whose transcript p shows.
func TestTranscriptIsTheMostRecentlyUpdatedTasks(t *testing.T) {
	conv := fixture()
	conv.Tasks = []client.Task{
		{ID: "t1", AgentType: "claude-code", Status: client.TaskAwaitingInput, UpdatedAt: at(3 * time.Minute)},
		{ID: "t2", AgentType: "codex", Status: client.TaskCompleted, UpdatedAt: at(6 * time.Minute)},
		{ID: "t3", AgentType: "claude-code", Status: client.TaskCompleted, UpdatedAt: at(time.Minute)},
	}
	h := opened(t, conv)
	h.api.turns = func(string, client.Page) (*client.Transcript, error) { return &client.Transcript{}, nil }
	h.key("p")

	if len(h.api.turnsCalls) != 1 || h.api.turnsCalls[0].task != "t2" {
		t.Errorf("transcript fetches = %+v, want t2's", h.api.turnsCalls)
	}
	h.has("transcript of codex", "no turns yet")
}

func TestTranscriptOfAConversationWithoutATaskSaysSo(t *testing.T) {
	conv := fixture()
	conv.Tasks = nil
	h := opened(t, conv)
	h.key("p")

	h.has("no task", "I fixed two things:")
	if len(h.api.turnsCalls) != 0 {
		t.Errorf("fetched a transcript with no task: %+v", h.api.turnsCalls)
	}
}

func TestTranscriptSaysLoadingThenTheServersError(t *testing.T) {
	h := opened(t, fixture())
	h.api.turns = func(string, client.Page) (*client.Transcript, error) {
		return nil, &client.Error{Status: http.StatusNotImplemented, Code: client.CodeNotImplemented, Message: "transcripts aren't kept on this server"}
	}
	h.hold()
	h.key("p")
	h.has("loading…")
	h.release()

	h.has("transcripts aren't kept on this server")
	h.lacks("loading…", "no turns yet")
}

func TestA401OnTheTranscriptSignsOut(t *testing.T) {
	h := opened(t, fixture())
	h.api.turns = func(string, client.Page) (*client.Transcript, error) {
		return nil, &client.Error{Status: http.StatusUnauthorized, Code: client.CodeUnauthorized, Message: "session expired"}
	}
	h.key("p")

	if len(h.out) != 1 {
		t.Fatalf("told the root %v, want one SignedOutMsg", h.out)
	}
	if _, ok := h.out[0].(SignedOutMsg); !ok || !h.stream.closed {
		t.Errorf("told the root %#v (stream closed %v), want SignedOutMsg and the stream closed", h.out[0], h.stream.closed)
	}
}

// A pane is what a terminal showed: it is drawn as text, whatever
// control sequences it holds.
func TestTranscriptDrawsAPaneAsText(t *testing.T) {
	tr := turns()
	tr.Turns[0].Pane = "\x1b]0;evil title\x07$ make\x1b[2J\x1b[H\r\n\x1b[31mFAIL\x1b[0m\ttests"
	tr.Turns[0].UserMessage = "run \x1b[5mmake\x1b[0m"
	h := withTurns(t, tr)
	h.key("p")

	h.has("$ make", "FAIL    tests", "run make")
	for _, seq := range []string{"\x1b]0;", "\x1b[2J", "\x1b[H", "\x1b[5m", "\x1b[31m", "\r"} {
		if strings.Contains(h.m.View(), seq) {
			t.Errorf("the view holds %q from the transcript", seq)
		}
	}
}

func TestTranscriptKeepsEveryCharacterOfAWidePane(t *testing.T) {
	tr := turns()
	wide := strings.Repeat("0123456789", 12) // a 120-column pane line
	tr.Turns[1].Pane = wide
	h := withTurns(t, tr)
	h.key("p")

	var pane strings.Builder
	for _, l := range h.lines() {
		if w := ansi.StringWidth(l); w > 80 {
			t.Errorf("a line is %d wide: %q", w, l)
		}
		if strings.Contains(l, "0123456789") {
			pane.WriteString(strings.Trim(l, " │"))
		}
	}
	if pane.String() != wide {
		t.Errorf("the pane line as shown is %q, want all of %q", pane.String(), wide)
	}
}

// manyTurns are turns from to to, "turn 01" on, with the ids u01 on.
func manyTurns(from, to int) []client.Turn {
	var out []client.Turn
	for i := from; i <= to; i++ {
		out = append(out, client.Turn{ID: fmt.Sprintf("u%02d", i), UserMessage: fmt.Sprintf("turn %02d", i),
			AgentMessage: "done", Pane: "$ true", CreatedAt: at(time.Duration(i) * time.Minute)})
	}
	return out
}

func TestTranscriptOpensAtTheLatestTurnAndLoadsOlderOnesAtTheTop(t *testing.T) {
	h := opened(t, fixture())
	h.api.turns = func(_ string, p client.Page) (*client.Transcript, error) {
		if p.Before == "u21" {
			return &client.Transcript{Turns: manyTurns(1, 20)}, nil
		}
		return &client.Transcript{Turns: manyTurns(21, 40), HasMore: true, NextBefore: "u21"}, nil
	}
	h.key("p")
	h.has("turn 40")
	h.lacks("turn 21", "↓ more below")

	h.hold()
	h.key("g")
	h.has("turn 21", "loading older turns…", "↓ more below")
	row := h.rowOf("turn 21")
	h.release()

	if want := []turnsCall{{"t1", client.Page{}}, {"t1", client.Page{Before: "u21"}}}; !slices.Equal(h.api.turnsCalls, want) {
		t.Fatalf("transcript fetches = %+v, want %+v", h.api.turnsCalls, want)
	}
	if got := h.rowOf("turn 21"); got != row || row < 0 {
		t.Errorf("turn 21 moved from row %d to row %d when the older page arrived:\n%s", row, got, h.screen())
	}
	h.lacks("turn 01", "loading older turns…")
	h.key("g")
	h.has("turn 01")
	h.key("G")
	h.has("turn 40")
}

// A turn is added to the transcript when it ends, which the stream
// reports as the task changing status.
func TestAnOpenTranscriptFollowsItsTask(t *testing.T) {
	tr := turns()
	h := withTurns(t, tr)
	h.key("p")
	before := len(h.api.turnsCalls)

	tr.Turns = append(tr.Turns, client.Turn{ID: "u3", UserMessage: "ship it", AgentMessage: "Shipped.", Pane: "$ git push", CreatedAt: at(9 * time.Minute)})
	h.event(taskUpdate("t1", client.TaskRunning, 9*time.Minute))

	if got := len(h.api.turnsCalls) - before; got != 1 {
		t.Errorf("%d transcript fetches after the task changed status, want 1", got)
	}
	h.has("ship it", "Shipped.")

	// Another task's change isn't this transcript's.
	h.event(taskUpdate("t7", client.TaskRunning, 9*time.Minute))
	if got := len(h.api.turnsCalls) - before; got != 1 {
		t.Errorf("%d transcript fetches after another task changed, want 1", got)
	}
}

// The stream replays nothing: a turn that ended while it was down is
// only found by fetching the transcript again when it is back.
func TestAnOpenTranscriptIsFetchedAgainWhenTheStreamReconnects(t *testing.T) {
	tr := turns()
	h := withTurns(t, tr)
	h.key("p")
	before := len(h.api.turnsCalls)

	h.event(client.StreamEvent{Type: client.EventDisconnected, Err: errors.New("eof"), RetryIn: time.Second})
	tr.Turns = append(tr.Turns, client.Turn{ID: "u3", UserMessage: "ship it", AgentMessage: "Shipped while the stream was down.", Pane: "$ git push", CreatedAt: at(9 * time.Minute)})
	h.event(client.StreamEvent{Type: client.EventConnected, Reconnected: true})

	if got := len(h.api.turnsCalls) - before; got != 1 {
		t.Errorf("%d transcript fetches after the reconnect, want 1", got)
	}
	h.has("Shipped while the stream was down.")
}

// A transcript's answer may arrive after it was left, or after another
// was opened: it isn't the one on screen.
func TestAnAnswerForATranscriptNoLongerShownIsDropped(t *testing.T) {
	h := withTurns(t, turns())
	h.hold()
	h.key("p")
	late := h.held
	h.held = nil
	h.holding = false
	h.key("esc")

	for _, c := range late {
		h.run(c)
	}
	h.has("I fixed two things:")
	h.lacks("$ go test")

	h.api.turns = func(string, client.Page) (*client.Transcript, error) { return nil, errors.New("the second one failed") }
	h.key("p")
	h.has("the second one failed")
	// The first opening's answer once more, now with a transcript open.
	h.api.turns = func(string, client.Page) (*client.Transcript, error) { return turns(), nil }
	for _, c := range late {
		h.run(c)
	}
	h.has("the second one failed")
	h.lacks("$ go test")
}

func TestHelpOnTheTranscriptShowsItsKeys(t *testing.T) {
	h := withTurns(t, turns())
	h.key("p", "?")
	h.has("Keys in a transcript", "move up", "back")
	h.lacks("show the task's transcript", "attach to the agent", "$ go test")

	h.key("esc")
	h.has("$ go test")
}

func TestTheTranscriptFillsTheWindowExactly(t *testing.T) {
	tr := turns()
	tr.Turns = append(tr.Turns, manyTurns(3, 12)...)
	h := withTurns(t, tr)
	h.key("p")
	for _, size := range [][2]int{{80, 24}, {120, 40}, {60, 15}, {20, 6}, {5, 2}, {1, 1}} {
		w, hgt := size[0], size[1]
		h.send(tea.WindowSizeMsg{Width: w, Height: hgt})
		lines := strings.Split(h.m.View(), "\n")
		if len(lines) != hgt {
			t.Errorf("%dx%d: %d lines", w, hgt, len(lines))
		}
		for i, l := range lines {
			if got := ansi.StringWidth(l); got > w {
				t.Errorf("%dx%d: line %d is %d wide: %q", w, hgt, i, got, ansi.Strip(l))
			}
		}
	}
}

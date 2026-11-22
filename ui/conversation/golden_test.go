package conversation

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Loomux/tui/client"
)

// Golden frames (the design's "Testing"): the screen as text at 80x24
// and 120x40, without its styling. Rewrite them after an intended change
// with
//
//	go test ./ui/conversation -run Golden -update
//
// and read the diff: a frame is the review of how the screen looks.
var update = flag.Bool("update", false, "rewrite the golden frames in testdata")

func checkGolden(t *testing.T, name string, h *harness) {
	t.Helper()
	lines := h.lines()
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (write it with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the screen (rewrite it with -update if the change is meant):\n--- screen\n%s--- %s\n%s", path, got, path, want)
	}
}

// chat is a conversation mid-turn: an exchange with a markdown reply, a
// second message whose turn is running, an agent at work and another
// stopped at a prompt.
func chat() *client.Conversation {
	started := at(8 * time.Minute)
	return &client.Conversation{
		ConversationID: "conv-1",
		Messages: []client.Message{
			userMsg("m1", "d1", "The ConversationPage test fails about one run in five on CI. Can you find out why and fix it?", time.Minute),
			replyMsg("m2", "d1", "## What was wrong\n\nThe test raced the stream's **first event**. Two fixes:\n\n"+
				"1. `waitFor` now waits for the `task_update`, not for a timeout.\n"+
				"2. The fake server sends its heartbeat *after* the first frame.\n\n"+
				"```sh\nnpm test -- ConversationPage --repeat 50\n```\n\n"+
				"All 50 runs pass. See [the stream's notes](https://example.net/docs/stream).", 3*time.Minute),
			userMsg("m3", "d2", "Good. Run the nightly suite against it too.", 8*time.Minute),
		},
		Tasks: []client.Task{
			{ID: "t1", Kind: "agent", AgentType: "claude-code", Status: client.TaskRunning, CreatedAt: at(time.Minute), UpdatedAt: at(8 * time.Minute)},
			{ID: "t2", Kind: "agent", AgentType: "codex", Status: client.TaskNeedsAttention, CreatedAt: at(2 * time.Minute), UpdatedAt: at(6 * time.Minute),
				Attention: &client.Attention{Kind: "permission", Title: "Bash command", Detail: "kubectl rollout restart deploy/api -n demo", Question: "Do you want to proceed?"}},
			{ID: "t3", Kind: "agent", AgentType: "claude-code", Status: client.TaskCompleted, CreatedAt: at(0), UpdatedAt: at(time.Minute)},
		},
		Dispatches: []client.Dispatch{
			{DispatchID: "d1", Status: client.DispatchSucceeded, CreatedAt: at(time.Minute)},
			{DispatchID: "d2", Status: client.DispatchRunning, CreatedAt: at(8 * time.Minute), StartedAt: &started},
		},
	}
}

func TestGoldenConversation(t *testing.T) {
	for name, size := range map[string][2]int{"conversation-80x24": {80, 24}, "conversation-120x40": {120, 40}} {
		t.Run(name, func(t *testing.T) {
			h := opened(t, chat(), func(o *Options) { o.Width, o.Height = size[0], size[1] })
			checkGolden(t, name, h)
		})
	}
}

// The stream has been down for five seconds and the last fetch failed:
// what was loaded stays, and the header says what is wrong.
func TestGoldenConversationReconnecting(t *testing.T) {
	h := opened(t, chat())
	h.event(client.StreamEvent{Type: client.EventDisconnected, RetryIn: time.Second})
	h.fire(0)
	h.key("g")
	checkGolden(t, "conversation-reconnecting-80x24", h)
}

func TestGoldenTranscript(t *testing.T) {
	for name, size := range map[string][2]int{"transcript-80x24": {80, 24}, "transcript-120x40": {120, 40}} {
		t.Run(name, func(t *testing.T) {
			h := opened(t, chat(), func(o *Options) { o.Width, o.Height = size[0], size[1] })
			h.api.turns = func(string, client.Page) (*client.Transcript, error) {
				return &client.Transcript{TaskID: "t1", Turns: []client.Turn{
					{ID: "u1", UserMessage: "The ConversationPage test fails about one run in five on CI. Find out why and fix it.",
						AgentMessage: "The test raced the stream's **first event**. Fixed in `ConversationPage.test.tsx`.",
						Pane:         "● Update(src/pages/ConversationPage.test.tsx)\n  ⎿  Updated with 6 additions and 3 removals\n\n● Bash(npm test -- ConversationPage --repeat 50)\n  ⎿  Tests  50 passed (50)\n\n> \n",
						CreatedAt:    at(3 * time.Minute)},
				}}, nil
			}
			h.key("p")
			checkGolden(t, name, h)
		})
	}
}

func TestGoldenHelp(t *testing.T) {
	h := opened(t, chat())
	h.key("?")
	checkGolden(t, "help-80x24", h)
}

// A window the root still draws (60x15 is the least it does): the
// transcript keeps its room and the tasks their lines.
func TestGoldenConversationSmallest(t *testing.T) {
	h := opened(t, chat())
	h.send(tea.WindowSizeMsg{Width: 60, Height: 15})
	checkGolden(t, "conversation-60x15", h)
}

package attach

import (
	"testing"
	"time"

	"github.com/Loomux/tui/client"
)

func TestTaskOf(t *testing.T) {
	at := func(min int) time.Time { return time.Date(2026, 11, 20, 14, min, 0, 0, time.UTC) }
	task := func(id, kind, status, class string, min int) client.Task {
		return client.Task{ID: id, Kind: kind, Status: status, ErrorClass: class, CreatedAt: at(min)}
	}
	for _, tc := range []struct {
		name  string
		tasks []client.Task
		want  string
	}{
		{"none", nil, ""},
		{"the open agent", []client.Task{
			task("done", "agent", client.TaskCompleted, "", 1),
			task("open", "agent", client.TaskAwaitingInput, "", 2),
		}, "open"},
		{"every open status", []client.Task{task("a", "agent", client.TaskRunning, "", 1)}, "a"},
		{"a prompt", []client.Task{task("a", "agent", client.TaskNeedsAttention, "", 1)}, "a"},
		{"one already driven", []client.Task{task("a", "agent", client.TaskHumanTakeover, "", 1)}, "a"},
		{"the newest of two open agents", []client.Task{
			task("new", "agent", client.TaskAwaitingInput, "", 5),
			task("old", "agent", client.TaskRunning, "", 2),
		}, "new"},
		{"an agent, not a shell or a command", []client.Task{
			task("shell", "shell", client.TaskRunning, "", 9),
			task("cmd", "command", client.TaskRunning, "", 8),
			task("agent", "agent", client.TaskAwaitingInput, "", 1),
		}, "agent"},
		{"only a shell", []client.Task{task("shell", "shell", client.TaskRunning, "", 9)}, ""},
		// A failed agent whose pane was kept for a person (to sign in, or
		// wait out a limit) can be attached to; an open one comes first.
		{"a failed agent with its pane kept", []client.Task{
			task("gone", "agent", client.TaskFailed, "agent_exited", 9),
			task("login", "agent", client.TaskFailed, "login_required", 3),
			task("limit", "agent", client.TaskFailed, "agent_rate_limited", 4),
		}, "limit"},
		{"an API error's pane", []client.Task{task("api", "agent", client.TaskFailed, "agent_api_error", 1)}, "api"},
		{"open before failed", []client.Task{
			task("login", "agent", client.TaskFailed, "login_required", 9),
			task("open", "agent", client.TaskAwaitingInput, "", 1),
		}, "open"},
		{"nothing to attach to", []client.Task{
			task("done", "agent", client.TaskCompleted, "", 1),
			task("gone", "agent", client.TaskFailed, "agent_exited", 2),
		}, ""},
	} {
		got, ok := TaskOf(tc.tasks)
		if got.ID != tc.want || ok != (tc.want != "") {
			t.Errorf("%s: TaskOf = %q, %v; want %q", tc.name, got.ID, ok, tc.want)
		}
	}
}

package attach

import "github.com/Loomux/tui/client"

// TaskOf picks the task of a conversation that attaching to the
// conversation means: its open agent task, the newest if there are
// several. Without one, it is the newest failed agent task whose pane
// the server kept for a person (to sign in, or to wait out a limit),
// which a takeover may hold too. It reports false when the conversation
// has neither.
func TaskOf(tasks []client.Task) (client.Task, bool) {
	var open, kept *client.Task
	newer := func(t, than *client.Task) bool { return than == nil || t.CreatedAt.After(than.CreatedAt) }
	for i := range tasks {
		t := &tasks[i]
		if t.Kind != "agent" {
			continue
		}
		switch t.Status {
		case client.TaskRunning, client.TaskAwaitingInput, client.TaskNeedsAttention, client.TaskHumanTakeover:
			if newer(t, open) {
				open = t
			}
		case client.TaskFailed:
			switch t.ErrorClass {
			case "login_required", "agent_rate_limited", "agent_api_error":
				if newer(t, kept) {
					kept = t
				}
			}
		}
	}
	switch {
	case open != nil:
		return *open, true
	case kept != nil:
		return *kept, true
	}
	return client.Task{}, false
}

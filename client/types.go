package client

import "time"

// Version is GET /version.
type Version struct {
	APIVersion    string `json:"api_version"`
	ServerVersion string `json:"server_version"`
}

// Task statuses, as the API spells them.
const (
	TaskRunning        = "running"
	TaskAwaitingInput  = "awaiting_input"
	TaskNeedsAttention = "needs_attention"
	TaskHumanTakeover  = "human_takeover"
	TaskCompleted      = "completed"
	TaskFailed         = "failed"
)

// Dispatch statuses.
const (
	DispatchQueued      = "queued"
	DispatchRunning     = "running"
	DispatchSucceeded   = "succeeded"
	DispatchFailed      = "failed"
	DispatchInterrupted = "interrupted"
)

// Message roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Hold modes.
const (
	ModeDrive = "drive"
	ModeWatch = "watch"
)

// ConversationPage is GET /conversations: the most recently updated
// first.
type ConversationPage struct {
	Conversations []ConversationSummary `json:"conversations"`
	HasMore       bool                  `json:"has_more"`
}

// ConversationSummary is one row of the conversation list.
type ConversationSummary struct {
	ConversationID string    `json:"conversation_id"`
	Preview        string    `json:"preview"`
	Status         string    `json:"status"`
	WorkspaceID    string    `json:"workspace_id"`
	UpdatedAt      time.Time `json:"updated_at"`

	// HeldBy, PendingConfirmationID (S6) and Watchers (S10) come with
	// later server releases than the pinned one, so they aren't in
	// testdata/uses.txt: they are read when a server sends them and
	// empty when it doesn't, and nothing relies on them being there.
	// The ticket that bumps the pin past them adds their lines.

	// HeldBy is who drives one of the conversation's tasks from a
	// terminal (its active drive hold), or nil.
	HeldBy *Holder `json:"held_by"`
	// PendingConfirmationID is the offer the conversation waits on a yes
	// or no to, or "".
	PendingConfirmationID string `json:"pending_confirmation_id"`
	// Watchers counts the watch holds on the conversation's tasks.
	Watchers int `json:"watchers"`
}

// Holder is a list row's held_by: who holds the conversation, and since
// when.
type Holder struct {
	ClientKind  string    `json:"client_kind"`
	ClientLabel string    `json:"client_label"`
	Since       time.Time `json:"since"`
}

// Conversation is GET /conversations/{id}: one page of its messages (the
// newest, or those before Page.Before) with its tasks, dispatches and
// offers.
type Conversation struct {
	ConversationID string         `json:"conversation_id"`
	Messages       []Message      `json:"messages"`
	Tasks          []Task         `json:"tasks"`
	Dispatches     []Dispatch     `json:"dispatches"`
	Confirmations  []Confirmation `json:"confirmations"`
	// HasMore says older messages exist: fetch them with NextBefore.
	HasMore    bool   `json:"has_more"`
	NextBefore string `json:"next_before"`
}

// Message is one message of a conversation.
type Message struct {
	ID         string    `json:"id"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	TaskID     string    `json:"task_id"`
	DispatchID string    `json:"dispatch_id"`
	CreatedAt  time.Time `json:"created_at"`
}

// Task is one of a conversation's tasks.
type Task struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Kind        string     `json:"kind"`
	AgentType   string     `json:"agent_type"`
	Status      string     `json:"status"`
	Command     string     `json:"command"`
	Attention   *Attention `json:"attention"`
	// ErrorClass, FailureReason and OutputTail say why a failed task
	// failed (login_required, agent_rate_limited, agent_api_error, …).
	ErrorClass    string     `json:"error_class"`
	FailureReason string     `json:"failure_reason"`
	OutputTail    string     `json:"output_tail"`
	ExitCode      *int       `json:"exit_code"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	StartedAt     *time.Time `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at"`
}

// Attention is the prompt a needs-attention task is stopped at.
type Attention struct {
	Kind     string            `json:"kind"`
	Title    string            `json:"title"`
	Detail   string            `json:"detail"`
	Question string            `json:"question"`
	Options  []AttentionOption `json:"options"`
	Selected int               `json:"selected"`
	Resets   string            `json:"resets"`
}

// AttentionOption is one answer to a prompt.
type AttentionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Dispatch is a dispatch job: a message sent, as it runs and ends.
type Dispatch struct {
	DispatchID     string     `json:"dispatch_id"`
	ConversationID string     `json:"conversation_id"`
	Status         string     `json:"status"`
	Reply          string     `json:"reply"`
	Error          string     `json:"error"`
	ErrorClass     string     `json:"error_class"`
	ConfirmationID string     `json:"confirmation_id"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at"`
}

// Done reports whether the dispatch has ended.
func (d *Dispatch) Done() bool {
	return d.Status == DispatchSucceeded || d.Status == DispatchFailed || d.Status == DispatchInterrupted
}

// Confirmation is an offer the router made (run a command, start an
// agent), waiting for the user's yes or resolved. Accept it by sending a
// message with its ID as SendRequest.ConfirmationID.
type Confirmation struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	StatusReason  string     `json:"status_reason"`
	Command       string     `json:"command"`
	AgentType     string     `json:"agent_type"`
	GitRemote     string     `json:"git_remote"`
	TargetID      string     `json:"target_id"`
	TargetName    string     `json:"target_name"`
	WorkspaceName string     `json:"workspace_name"`
	DispatchID    string     `json:"dispatch_id"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	ResolvedAt    *time.Time `json:"resolved_at"`
}

// AttachInfo is how to attach to a task's tmux session: GET
// /tasks/{id}/attach-info, and a takeover's attach.
type AttachInfo struct {
	TaskID      string `json:"task_id"`
	TmuxSession string `json:"tmux_session"`
	TmuxSocket  string `json:"tmux_socket"`
	// AttachCommand is the preferred command as one shell line;
	// AttachCommands are every way, each with an argv when the server
	// built one (run that rather than splitting the line).
	AttachCommand  string          `json:"attach_command"`
	AttachCommands []AttachCommand `json:"attach_commands"`
	Target         AttachTarget    `json:"target"`
}

// AttachCommand is one way to attach.
type AttachCommand struct {
	Via     string   `json:"via"`
	Command string   `json:"command"`
	Argv    []string `json:"argv"`
}

// AttachTarget is the machine the task runs on.
type AttachTarget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Host string `json:"host"`
	User string `json:"user"`
}

// Hold is a session's lease on a task: a drive hold (the task is in
// human_takeover) or a watch hold.
type Hold struct {
	ID          string     `json:"id"`
	TaskID      string     `json:"task_id"`
	SessionID   string     `json:"session_id"`
	Mode        string     `json:"mode"`
	ClientKind  string     `json:"client_kind"`
	ClientLabel string     `json:"client_label"`
	PrevStatus  string     `json:"prev_status"`
	TTLSeconds  int        `json:"ttl_seconds"`
	AcquiredAt  time.Time  `json:"acquired_at"`
	RenewedAt   time.Time  `json:"renewed_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	EndedAt     *time.Time `json:"ended_at"`
	EndedReason string     `json:"ended_reason"`
}

// TakeoverRequest is POST /tasks/{id}/takeover's body.
type TakeoverRequest struct {
	// Mode is ModeDrive (the default) or ModeWatch. A server without
	// watch holds answers a watch with CodeUnsupportedMode.
	Mode string `json:"mode,omitempty"`
	// ClientLabel names this client to other clients ("laptop").
	ClientLabel string `json:"client_label,omitempty"`
	// Force takes the task from another session's hold.
	Force bool `json:"force,omitempty"`
	// TTLSeconds is the lease; zero is the server's default.
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

// Takeover is a granted takeover: the hold and how to attach.
type Takeover struct {
	TaskID string     `json:"task_id"`
	Hold   *Hold      `json:"hold"`
	Attach AttachInfo `json:"attach"`
}

// Release is POST /tasks/{id}/release: the task's status after it, and
// the hold as it ended (nil when there was none).
type Release struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
	Hold   *Hold  `json:"hold"`
}

// Cancelled is a cancel's answer: what it stopped.
type Cancelled struct {
	DispatchID string `json:"dispatch_id"`
	TaskID     string `json:"task_id"`
}

// Transcript is GET /tasks/{id}/transcript: one page of the task's
// turns.
type Transcript struct {
	TaskID     string `json:"task_id"`
	Turns      []Turn `json:"turns"`
	HasMore    bool   `json:"has_more"`
	NextBefore string `json:"next_before"`
}

// Turn is one exchange with the agent.
type Turn struct {
	ID           string    `json:"id"`
	UserMessage  string    `json:"user_message"`
	AgentMessage string    `json:"agent_message"`
	Pane         string    `json:"pane"`
	CreatedAt    time.Time `json:"created_at"`
}

// Session is one signed-in session of the user.
type Session struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	ClientKind string    `json:"client_kind"`
	Current    bool      `json:"current"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// Page selects a page of a conversation or a transcript: Limit items (the
// server's default when zero) before the cursor Before (the newest when
// empty).
type Page struct {
	Before string
	Limit  int
}

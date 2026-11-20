package client_test

// Contract tests: every call of the client against a real api.Server
// from the pinned server module, through its api/apitest (S9): a
// temporary SQLite store, a stub dispatcher and a fake target executor.
// They catch field drift a hand-written fake can't. The nightly run
// (.github/workflows/server-main.yml) repeats them against the server's
// main.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Loomux/server/api/apitest"
	"github.com/Loomux/server/registry"

	"github.com/Loomux/tui/client"
)

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

// newClient returns a client of srv signed in as a linked terminal
// session, as `loomux login` leaves one.
func newClient(t *testing.T, srv *apitest.Server) *client.Client {
	t.Helper()
	return clientWith(t, srv, srv.SeedLinkedSession("laptop", registry.SessionKindTUI))
}

func clientWith(t *testing.T, srv *apitest.Server, token string) *client.Client {
	t.Helper()
	c, err := client.New(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestContractVersion(t *testing.T) {
	srv := apitest.New(t)
	v, err := clientWith(t, srv, "").Version(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if v.APIVersion != client.APIVersion {
		t.Errorf("api_version = %q, want %q", v.APIVersion, client.APIVersion)
	}
	if v.ServerVersion == "" {
		t.Error("no server_version")
	}
}

func TestContractUnauthorized(t *testing.T) {
	srv := apitest.New(t)
	_, err := clientWith(t, srv, "not-a-token").Conversations(ctx(t), 0)
	if !client.Unauthorized(err) || !client.Is(err, client.CodeUnauthorized) {
		t.Fatalf("err = %v, want the 401", err)
	}
}

func TestContractConversations(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	older := srv.SeedConversation("", "first question", "first answer")
	newer := srv.SeedPrompt("", registry.Attention{})
	page, err := c.Conversations(ctx(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Conversations) != 2 || page.HasMore {
		t.Fatalf("page = %+v, want both conversations", page)
	}
	ids := map[string]client.ConversationSummary{}
	for _, row := range page.Conversations {
		ids[row.ConversationID] = row
		if row.UpdatedAt.IsZero() || row.Status == "" || row.WorkspaceID == "" {
			t.Errorf("row %+v: missing fields", row)
		}
	}
	if _, ok := ids[older.ConversationID]; !ok {
		t.Errorf("rows %+v lack %s", page.Conversations, older.ConversationID)
	}
	if row := ids[newer.ConversationID]; row.Status != client.TaskNeedsAttention {
		t.Errorf("prompt row status %q, want needs_attention", row.Status)
	}

	one, err := c.Conversations(ctx(t), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Conversations) != 1 || !one.HasMore {
		t.Errorf("limit 1: %+v, want one row and has_more", one)
	}
}

func TestContractConversation(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	task := srv.SeedConversation("", "what's in build/?", "two files")
	conv := task.ConversationID
	prompt := srv.SeedPrompt(conv, registry.Attention{})
	failed := srv.SeedFailedTask(conv, registry.ErrorClassLoginRequired, "the agent isn't logged in")
	offer := srv.SeedOffer(registry.Confirmation{ConversationID: conv, Command: "make test"})

	got, err := c.Conversation(ctx(t), conv, client.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ConversationID != conv {
		t.Errorf("conversation_id %q", got.ConversationID)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != client.RoleUser || got.Messages[0].Content != "what's in build/?" ||
		got.Messages[1].Role != client.RoleAssistant || got.Messages[1].TaskID != task.ID || got.Messages[1].ID == "" || got.Messages[1].CreatedAt.IsZero() {
		t.Errorf("messages = %+v", got.Messages)
	}
	tasks := map[string]client.Task{}
	for _, tk := range got.Tasks {
		tasks[tk.ID] = tk
	}
	p := tasks[prompt.ID]
	if p.Status != client.TaskNeedsAttention || p.Attention == nil || p.Attention.Question == "" || len(p.Attention.Options) != 2 || p.Attention.Options[0].Label != "Yes" {
		t.Errorf("prompt task = %+v, attention %+v", p, p.Attention)
	}
	f := tasks[failed.ID]
	if f.Status != client.TaskFailed || f.ErrorClass != "login_required" || f.FailureReason != "the agent isn't logged in" {
		t.Errorf("failed task = %+v", f)
	}
	if tk := tasks[task.ID]; tk.Status != client.TaskAwaitingInput || tk.AgentType == "" || tk.WorkspaceID == "" || tk.CreatedAt.IsZero() {
		t.Errorf("task = %+v", tk)
	}
	if len(got.Confirmations) != 1 {
		t.Fatalf("confirmations = %+v", got.Confirmations)
	}
	if o := got.Confirmations[0]; o.ID != offer.ID || o.Kind != "run_command" || o.Status != "pending" || o.Command != "make test" || o.ExpiresAt.IsZero() {
		t.Errorf("offer = %+v", o)
	}

	// Paging back: the newest message first, then the one before it.
	last, err := c.Conversation(ctx(t), conv, client.Page{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Messages) != 1 || !last.HasMore || last.NextBefore == "" || last.Messages[0].Role != client.RoleAssistant {
		t.Fatalf("limit 1: %+v", last)
	}
	prev, err := c.Conversation(ctx(t), conv, client.Page{Limit: 1, Before: last.NextBefore})
	if err != nil {
		t.Fatal(err)
	}
	if len(prev.Messages) != 1 || prev.Messages[0].Role != client.RoleUser {
		t.Errorf("before %q: %+v", last.NextBefore, prev.Messages)
	}
}

// waitDispatch polls a dispatch until it ends.
func waitDispatch(t *testing.T, c *client.Client, id string) *client.Dispatch {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, err := c.Dispatch(ctx(t), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Done() {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("dispatch %s still %s", id, d.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestContractSend(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	srv.Dispatcher.Reply("hello back")
	d, err := c.Send(ctx(t), client.SendRequest{Message: "hello", WorkspaceHint: "ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	if d.DispatchID == "" || d.ConversationID == "" || d.Status == "" {
		t.Fatalf("dispatch = %+v", d)
	}
	done := waitDispatch(t, c, d.DispatchID)
	if done.Status != client.DispatchSucceeded || done.Reply != "hello back" || done.ConversationID != d.ConversationID || done.FinishedAt == nil {
		t.Errorf("done = %+v", done)
	}
	seen := srv.Dispatcher.Dispatches()
	if len(seen) != 1 || seen[0].Message != "hello" || seen[0].WorkspaceHint != "ws-1" {
		t.Errorf("the server saw %+v", seen)
	}
	conv, err := c.Conversation(ctx(t), d.ConversationID, client.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if len(conv.Dispatches) != 1 || conv.Dispatches[0].DispatchID != d.DispatchID || conv.Dispatches[0].Status != client.DispatchSucceeded {
		t.Errorf("dispatches = %+v", conv.Dispatches)
	}
}

func TestContractSendAcceptsAnOffer(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	offer := srv.SeedOffer(registry.Confirmation{Command: "make test"})
	d, err := c.Send(ctx(t), client.SendRequest{ConversationID: offer.ConversationID, Message: "yes", ConfirmationID: offer.ID})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, c, d.DispatchID)
	if seen := srv.Dispatcher.Dispatches(); len(seen) != 1 || seen[0].ConfirmationID != offer.ID {
		t.Errorf("the server saw %+v", seen)
	}
}

// The same Idempotency-Key and body returns the first dispatch and sends
// nothing; the same key with another body is idempotency_conflict.
func TestContractSendIdempotency(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	conv := srv.SeedConversation("", "q", "a").ConversationID
	key := client.NewIdempotencyKey()
	first, err := c.Send(ctx(t), client.SendRequest{ConversationID: conv, Message: "once", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, c, first.DispatchID)
	again, err := c.Send(ctx(t), client.SendRequest{ConversationID: conv, Message: "once", IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if again.DispatchID != first.DispatchID {
		t.Errorf("repeat got dispatch %s, want the first's %s", again.DispatchID, first.DispatchID)
	}
	if n := len(srv.Dispatcher.Dispatches()); n != 1 {
		t.Errorf("%d dispatches ran, want 1", n)
	}
	_, err = c.Send(ctx(t), client.SendRequest{ConversationID: conv, Message: "different", IdempotencyKey: key})
	if !client.Is(err, client.CodeIdempotencyConflict) {
		t.Errorf("err = %v, want idempotency_conflict", err)
	}
}

func TestContractSendBusyAndCancel(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	release := srv.Dispatcher.Block()
	defer release()
	conv := srv.SeedConversation("", "q", "a").ConversationID
	first, err := c.Send(ctx(t), client.SendRequest{ConversationID: conv, Message: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Send(ctx(t), client.SendRequest{ConversationID: conv, Message: "too soon"})
	var e *client.Error
	if !errors.As(err, &e) || e.Code != client.CodeConversationBusy || e.Status != 409 || e.DispatchID != first.DispatchID || e.Message == "" {
		t.Fatalf("err = %#v, want conversation_busy naming %s", err, first.DispatchID)
	}
	got, err := c.CancelDispatch(ctx(t), first.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DispatchID != first.DispatchID {
		t.Errorf("cancel = %+v", got)
	}
	if d := waitDispatch(t, c, first.DispatchID); d.Status == client.DispatchSucceeded {
		t.Errorf("a cancelled dispatch succeeded: %+v", d)
	}
	if _, err := c.CancelDispatch(ctx(t), "no-such-dispatch"); !client.Is(err, client.CodeNotFound) {
		t.Errorf("unknown dispatch: %v, want not_found", err)
	}
}

func TestContractCancelTask(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	task := srv.SeedConversation("", "q", "a")
	got, err := c.CancelTask(ctx(t), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "" && got.TaskID != task.ID {
		t.Errorf("cancel = %+v", got)
	}
	if st := srv.Task(task.ID).Status; st != registry.TaskStatusFailed {
		t.Errorf("task status %q after cancel, want failed", st)
	}
	if _, err := c.CancelTask(ctx(t), task.ID); !client.Is(err, client.CodeConflict) {
		t.Errorf("cancelling an ended task: %v, want a 409", err)
	}
}

func TestContractAttachInfo(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	ws := srv.SeedWorkspace("web", srv.SeedTarget("box"))
	task := srv.SeedTask(apitest.Task{WorkspaceID: ws.ID})
	a, err := c.AttachInfo(ctx(t), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.TaskID != task.ID || a.TmuxSession == "" || a.TmuxSocket != apitest.TmuxSocket || a.AttachCommand == "" {
		t.Errorf("attach-info = %+v", a)
	}
	if a.Target.Name != "box" || a.Target.Host != "box.test" || a.Target.User != "loomux" || a.Target.Kind == "" || a.Target.ID == "" {
		t.Errorf("target = %+v", a.Target)
	}
	if len(a.AttachCommands) == 0 || a.AttachCommands[0].Via == "" || a.AttachCommands[0].Command == "" {
		t.Fatalf("attach_commands = %+v", a.AttachCommands)
	}
	var ssh *client.AttachCommand
	for i := range a.AttachCommands {
		if a.AttachCommands[i].Via == "ssh" {
			ssh = &a.AttachCommands[i]
		}
	}
	if ssh == nil || len(ssh.Argv) == 0 || ssh.Argv[0] != "ssh" {
		t.Errorf("no ssh command with an argv in %+v", a.AttachCommands)
	}
	if _, err := c.AttachInfo(ctx(t), "no-such-task"); !client.Is(err, client.CodeNotFound) {
		t.Errorf("unknown task: %v, want not_found", err)
	}
}

// Takeover, heartbeat and release, with another session's hold in the
// way, and the codes each one branches on.
func TestContractHolds(t *testing.T) {
	srv := apitest.New(t)
	laptop := newClient(t, srv)
	desk := clientWith(t, srv, srv.SeedLinkedSession("desk", registry.SessionKindTUI))
	task := srv.SeedConversation("", "q", "a")

	tk, err := laptop.Takeover(ctx(t), task.ID, client.TakeoverRequest{ClientLabel: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	h := tk.Hold
	if tk.TaskID != task.ID || h == nil || h.ID == "" || h.Mode != client.ModeDrive || h.ClientLabel != "laptop" || h.ClientKind != registry.SessionKindTUI ||
		h.PrevStatus != client.TaskAwaitingInput || h.TTLSeconds <= 0 || !h.ExpiresAt.After(h.AcquiredAt) || h.TaskID != task.ID || h.SessionID == "" {
		t.Fatalf("takeover = %+v, hold %+v", tk, h)
	}
	if tk.Attach.TaskID != task.ID || tk.Attach.TmuxSession == "" || len(tk.Attach.AttachCommands) == 0 {
		t.Errorf("takeover attach = %+v", tk.Attach)
	}

	renewed, err := laptop.Heartbeat(ctx(t), task.ID, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.ID != h.ID || renewed.RenewedAt.Before(h.RenewedAt) {
		t.Errorf("heartbeat = %+v", renewed)
	}

	_, err = desk.Takeover(ctx(t), task.ID, client.TakeoverRequest{ClientLabel: "desk"})
	var e *client.Error
	if !errors.As(err, &e) || e.Code != client.CodeTaskHeld || e.Hold == nil || e.Hold.ClientLabel != "laptop" {
		t.Fatalf("a held task: %#v, want task_held with laptop's hold", err)
	}

	_, err = laptop.Takeover(ctx(t), task.ID, client.TakeoverRequest{Mode: client.ModeWatch})
	if !client.Is(err, client.CodeUnsupportedMode) && err != nil {
		t.Errorf("watch: %v, want unsupported_mode (a server with watch holds grants one)", err)
	}

	rel, err := laptop.Release(ctx(t), task.ID, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rel.TaskID != task.ID || rel.Status != client.TaskAwaitingInput || rel.Hold == nil || rel.Hold.EndedAt == nil || rel.Hold.EndedReason == "" {
		t.Errorf("release = %+v, hold %+v", rel, rel.Hold)
	}

	_, err = laptop.Heartbeat(ctx(t), task.ID, h.ID)
	if !errors.As(err, &e) || e.Code != client.CodeHoldEnded || e.Status != 410 || e.EndedReason == "" || e.Hold == nil || e.Hold.ID != h.ID {
		t.Errorf("heartbeat after release: %#v, want hold_ended with its reason", err)
	}

	// Taken by force, the first holder's heartbeat learns it ended.
	tk2, err := desk.Takeover(ctx(t), task.ID, client.TakeoverRequest{ClientLabel: "desk"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := laptop.Takeover(ctx(t), task.ID, client.TakeoverRequest{ClientLabel: "laptop", Force: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := desk.Heartbeat(ctx(t), task.ID, tk2.Hold.ID); !client.Is(err, client.CodeHoldEnded) {
		t.Errorf("heartbeat of a hold taken by force: %v, want hold_ended", err)
	}

	// Take back: release without a hold id ends whoever holds it.
	if _, err := desk.Release(ctx(t), task.ID, ""); err != nil {
		t.Errorf("take back: %v", err)
	}
	if hold := srv.ActiveHold(task.ID); hold != nil {
		t.Errorf("still held after take back: %+v", hold)
	}
}

func TestContractTakeoverInactiveTask(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	done := srv.SeedTask(apitest.Task{Status: registry.TaskStatusCompleted})
	if _, err := c.Takeover(ctx(t), done.ID, client.TakeoverRequest{}); !client.Is(err, client.CodeTaskInactive) {
		t.Errorf("err = %v, want task_inactive", err)
	}
}

func TestContractTranscript(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	task := srv.SeedConversation("", "q", "a")
	srv.SeedTurn(task, "do it", "done", "$ make\nok")
	srv.SeedTurn(task, "again", "done again", "$ make\nok")
	tr, err := c.Transcript(ctx(t), task.ID, client.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if tr.TaskID != task.ID || len(tr.Turns) != 2 || tr.HasMore {
		t.Fatalf("transcript = %+v", tr)
	}
	for _, turn := range tr.Turns {
		if turn.ID == "" || turn.UserMessage == "" || turn.AgentMessage == "" || turn.Pane == "" || turn.CreatedAt.IsZero() {
			t.Errorf("turn = %+v", turn)
		}
	}
	one, err := c.Transcript(ctx(t), task.ID, client.Page{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Turns) != 1 || !one.HasMore || one.NextBefore == "" {
		t.Errorf("limit 1: %+v", one)
	}
}

func TestContractSessionsAndLogout(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	srv.Login() // the browser's session
	ss, err := c.Sessions(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("sessions = %+v", ss)
	}
	var current []client.Session
	for _, s := range ss {
		if s.ID == "" || s.CreatedAt.IsZero() || s.LastUsedAt.IsZero() {
			t.Errorf("session = %+v", s)
		}
		if s.Current {
			current = append(current, s)
		}
	}
	if len(current) != 1 || current[0].Label != "laptop" || current[0].ClientKind != registry.SessionKindTUI {
		t.Errorf("current = %+v", current)
	}
	if err := c.Logout(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sessions(ctx(t)); !client.Unauthorized(err) {
		t.Errorf("after logout: %v, want the 401", err)
	}
}

func next(t *testing.T, s *client.Stream, want string) client.StreamEvent {
	t.Helper()
	for {
		ev, err := s.Next(ctx(t))
		if err != nil {
			t.Fatalf("waiting for %s: %v", want, err)
		}
		if ev.Type == want {
			return ev
		}
		if ev.Type == client.EventDisconnected {
			t.Fatalf("waiting for %s: disconnected: %v", want, ev.Err)
		}
	}
}

// The stream reports a send's dispatch, its reply message and task
// status changes, and ends with the session.
func TestContractStream(t *testing.T) {
	srv := apitest.New(t)
	c := newClient(t, srv)
	task := srv.SeedConversation("", "q", "a")
	conv := task.ConversationID
	s := c.OpenStream(conv)
	defer s.Close()
	if ev := next(t, s, client.EventConnected); ev.Reconnected {
		t.Errorf("first connect reported as a reconnect")
	}

	srv.SetTaskStatus(task.ID, registry.TaskStatusRunning, nil)
	ev := next(t, s, client.EventTaskUpdate)
	for ev.Task.Status != client.TaskRunning {
		ev = next(t, s, client.EventTaskUpdate)
	}
	if ev.Task.TaskID != task.ID || ev.Task.WorkspaceID == "" || ev.Task.UpdatedAt.IsZero() {
		t.Errorf("task_update = %+v", ev.Task)
	}

	srv.Dispatcher.Reply("streamed")
	d, err := c.Send(ctx(t), client.SendRequest{ConversationID: conv, Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	du := next(t, s, client.EventDispatchUpdate)
	for du.Dispatch.Status != client.DispatchSucceeded {
		du = next(t, s, client.EventDispatchUpdate)
	}
	if du.Dispatch.DispatchID != d.DispatchID || du.Dispatch.Reply != "streamed" || du.Dispatch.UpdatedAt.IsZero() {
		t.Errorf("dispatch_update = %+v", du.Dispatch)
	}
	var reply *client.MessageAdded
	deadline := time.Now().Add(5 * time.Second)
	for reply == nil && time.Now().Before(deadline) {
		m := next(t, s, client.EventMessageAdded)
		if m.Message.Role == client.RoleAssistant {
			reply = m.Message
		}
	}
	if reply == nil || reply.MessageID == "" || reply.CreatedAt.IsZero() {
		t.Fatalf("message_added = %+v", reply)
	}

	srv.SetTaskStatus(task.ID, registry.TaskStatusFailed, nil)
	for {
		ev := next(t, s, client.EventTaskUpdate)
		if ev.Task.Status == client.TaskFailed {
			break
		}
	}

	// Logging out ends the session's streams; reconnecting is refused
	// with the 401.
	if err := c.Logout(ctx(t)); err != nil {
		t.Fatal(err)
	}
	for {
		ev, err := s.Next(ctx(t))
		if client.Unauthorized(err) {
			break
		}
		if err != nil {
			t.Fatalf("after logout: %v, want the 401", err)
		}
		if ev.Type == client.EventConnected {
			t.Fatal("reconnected after logout")
		}
	}
}

// The test server's address is loopback http://, which the HTTPS-only
// rule allows only there.
func TestContractServerIsLoopback(t *testing.T) {
	srv := apitest.New(t)
	if !strings.HasPrefix(srv.URL, "http://127.0.0.1:") {
		t.Skipf("apitest URL %s", srv.URL)
	}
	if c := clientWith(t, srv, ""); c.Server() != srv.URL {
		t.Errorf("Server() = %q, want %q", c.Server(), srv.URL)
	}
}

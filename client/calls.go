package client

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Version is GET /version. It needs no token.
func (c *Client) Version(ctx context.Context) (*Version, error) {
	var v Version
	err := c.do(ctx, request{method: http.MethodGet, path: "/version", ok: []int{200}, out: &v})
	return ok(&v, err)
}

// Conversations is GET /conversations: up to limit (the server's default
// when zero) of the most recently updated.
func (c *Client) Conversations(ctx context.Context, limit int) (*ConversationPage, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var p ConversationPage
	err := c.do(ctx, request{method: http.MethodGet, path: "/conversations", query: q, ok: []int{200}, out: &p})
	return ok(&p, err)
}

// Conversation is GET /conversations/{id}, one page of its messages.
func (c *Client) Conversation(ctx context.Context, id string, page Page) (*Conversation, error) {
	var conv Conversation
	err := c.do(ctx, request{method: http.MethodGet, path: "/conversations/" + seg(id), query: page.query(),
		ok: []int{200}, out: &conv})
	return ok(&conv, err)
}

// SendRequest is a message to send: POST /dispatch.
type SendRequest struct {
	// ConversationID is the conversation; empty starts a new one, whose
	// id is in the answer.
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
	// WorkspaceHint is advisory: the router decides.
	WorkspaceHint string `json:"workspace_hint,omitempty"`
	// ConfirmationID accepts that offer.
	ConfirmationID string `json:"confirmation_id,omitempty"`
	// IdempotencyKey makes a repeat of the same request return the
	// original dispatch instead of sending the message again. Send makes
	// one when it's empty; a caller that may resend the same message
	// later (after a crash, or a send it couldn't confirm) sets its own
	// with NewIdempotencyKey and reuses it.
	IdempotencyKey string `json:"-"`
}

// Send is POST /dispatch, asynchronous: the dispatch as queued, to follow
// on the conversation's stream or with Dispatch. A conversation with a
// dispatch in flight answers CodeConversationBusy (Error.DispatchID names
// it).
//
// A request that may not have reached the server, or that a server
// shutting down refused, is sent again (twice at most) with the same
// Idempotency-Key, so the message is never sent twice. An answer is
// never retried.
func (c *Client) Send(ctx context.Context, m SendRequest) (*Dispatch, error) {
	key := m.IdempotencyKey
	if key == "" {
		key = NewIdempotencyKey()
	}
	h := http.Header{}
	h.Set("Idempotency-Key", key)
	h.Set("Prefer", "respond-async")
	var d Dispatch
	var err error
	for attempt := 0; ; attempt++ {
		d = Dispatch{}
		err = c.do(ctx, request{method: http.MethodPost, path: "/dispatch", body: m, header: h,
			ok: []int{http.StatusOK, http.StatusAccepted}, out: &d})
		if err == nil || attempt >= c.retries || !retryable(err) {
			break
		}
		t := time.NewTimer(c.retryWait << attempt)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
	return ok(&d, err)
}

// NewIdempotencyKey returns a fresh Idempotency-Key: 128 random bits,
// base64url.
func NewIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}

// Dispatch is GET /dispatches/{id}.
func (c *Client) Dispatch(ctx context.Context, id string) (*Dispatch, error) {
	var d Dispatch
	err := c.do(ctx, request{method: http.MethodGet, path: "/dispatches/" + seg(id), ok: []int{200}, out: &d})
	return ok(&d, err)
}

// CancelDispatch is POST /dispatches/{id}/cancel.
func (c *Client) CancelDispatch(ctx context.Context, id string) (*Cancelled, error) {
	var r Cancelled
	err := c.do(ctx, request{method: http.MethodPost, path: "/dispatches/" + seg(id) + "/cancel",
		ok: []int{http.StatusAccepted}, out: &r})
	return ok(&r, err)
}

// CancelTask is POST /tasks/{id}/cancel.
func (c *Client) CancelTask(ctx context.Context, id string) (*Cancelled, error) {
	var r Cancelled
	err := c.do(ctx, request{method: http.MethodPost, path: "/tasks/" + seg(id) + "/cancel",
		ok: []int{http.StatusAccepted}, out: &r})
	return ok(&r, err)
}

// AttachInfo is GET /tasks/{id}/attach-info.
func (c *Client) AttachInfo(ctx context.Context, taskID string) (*AttachInfo, error) {
	var a AttachInfo
	err := c.do(ctx, request{method: http.MethodGet, path: "/tasks/" + seg(taskID) + "/attach-info",
		ok: []int{200}, out: &a})
	return ok(&a, err)
}

// Takeover is POST /tasks/{id}/takeover. Another session's hold answers
// CodeTaskHeld with that hold in Error.Hold; a server without the route
// answers 404 (CodeNotFound): it can't attach yet, and the caller shows
// the attach command as text instead.
func (c *Client) Takeover(ctx context.Context, taskID string, r TakeoverRequest) (*Takeover, error) {
	var t Takeover
	err := c.do(ctx, request{method: http.MethodPost, path: "/tasks/" + seg(taskID) + "/takeover", body: r,
		ok: []int{200}, out: &t})
	return ok(&t, err)
}

// Heartbeat is POST /tasks/{id}/takeover/heartbeat: the hold renewed. An
// ended hold answers CodeHoldEnded, with Error.EndedReason and the hold.
func (c *Client) Heartbeat(ctx context.Context, taskID, holdID string) (*Hold, error) {
	var r struct {
		Hold *Hold `json:"hold"`
	}
	err := c.do(ctx, request{method: http.MethodPost, path: "/tasks/" + seg(taskID) + "/takeover/heartbeat",
		body: map[string]string{"hold_id": holdID}, ok: []int{200}, out: &r})
	if err != nil {
		return nil, err
	}
	return r.Hold, nil
}

// Release is POST /tasks/{id}/release: holdID ends this session's hold,
// and an empty holdID takes the task back from whoever holds it.
func (c *Client) Release(ctx context.Context, taskID, holdID string) (*Release, error) {
	body := map[string]string{}
	if holdID != "" {
		body["hold_id"] = holdID
	}
	var r Release
	err := c.do(ctx, request{method: http.MethodPost, path: "/tasks/" + seg(taskID) + "/release", body: body,
		ok: []int{200}, out: &r})
	return ok(&r, err)
}

// Transcript is GET /tasks/{id}/transcript, one page of turns.
func (c *Client) Transcript(ctx context.Context, taskID string, page Page) (*Transcript, error) {
	var t Transcript
	err := c.do(ctx, request{method: http.MethodGet, path: "/tasks/" + seg(taskID) + "/transcript",
		query: page.query(), ok: []int{200}, out: &t})
	return ok(&t, err)
}

// Sessions is GET /sessions: the user's sessions, this one Current.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var r struct {
		Sessions []Session `json:"sessions"`
	}
	err := c.do(ctx, request{method: http.MethodGet, path: "/sessions", ok: []int{200}, out: &r})
	if err != nil {
		return nil, err
	}
	return r.Sessions, nil
}

// Logout is POST /logout: this session ends, and with it its streams and
// holds.
func (c *Client) Logout(ctx context.Context) error {
	return c.do(ctx, request{method: http.MethodPost, path: "/logout", ok: []int{http.StatusNoContent}})
}

func (p Page) query() url.Values {
	q := url.Values{}
	if p.Before != "" {
		q.Set("before", p.Before)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	return q
}

// ok returns v, or nil and err.
func ok[T any](v *T, err error) (*T, error) {
	if err != nil {
		return nil, err
	}
	return v, nil
}

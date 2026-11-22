package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// sseEvent is one Server-Sent Event: its type ("message" when the server
// named none) and its data lines joined by "\n".
type sseEvent struct {
	Type string
	Data string
}

// sseReader reads Server-Sent Events (the WHATWG format, as
// api/streams.go writes it): "event:" and "data:" fields, ":" comments
// (the server's heartbeat), and a blank line ending each event. "id:" and
// "retry:" are ignored: the server sends neither, and this client's
// backoff is its own.
type sseReader struct {
	r   *bufio.Reader
	max int
	// onLine runs after every line read, comments included: the stream's
	// idle timer.
	onLine func()
}

func newSSEReader(r io.Reader, max int) *sseReader {
	return &sseReader{r: bufio.NewReader(r), max: max}
}

// next returns the next event with data. At the end of the stream it
// returns io.EOF, dropping an event the end cut off.
func (s *sseReader) next() (sseEvent, error) {
	var typ string
	var data bytes.Buffer
	hasData := false
	for {
		line, err := s.line()
		if err != nil {
			return sseEvent{}, err
		}
		if s.onLine != nil {
			s.onLine()
		}
		if line == "" {
			if hasData {
				if typ == "" {
					typ = "message"
				}
				return sseEvent{Type: typ, Data: data.String()}, nil
			}
			typ = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			typ = value
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasData = true
			if data.Len() > s.max {
				return sseEvent{}, fmt.Errorf("a stream event is too large (over %d bytes)", s.max)
			}
		}
	}
}

// line reads one line without its "\n" or "\r\n", at most s.max bytes.
func (s *sseReader) line() (string, error) {
	var b []byte
	for {
		chunk, err := s.r.ReadSlice('\n')
		b = append(b, chunk...)
		if len(b) > s.max {
			return "", fmt.Errorf("a stream event is too large (over %d bytes)", s.max)
		}
		switch {
		case err == nil:
			return strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return "", io.EOF // a last line with no end isn't a line
		default:
			return "", err
		}
	}
}

// Stream event types: the server's, and two of the Stream's own.
const (
	// EventConnected: the stream is open. Reconnected says it was open
	// before and dropped: events in between are lost (message_added isn't
	// replayed), so refetch the conversation.
	EventConnected = "connected"
	// EventDisconnected: the stream dropped (Err says why) and Next
	// reconnects after RetryIn.
	EventDisconnected = "disconnected"

	EventTaskUpdate     = "task_update"
	EventMessageAdded   = "message_added"
	EventDispatchUpdate = "dispatch_update"
)

// TaskUpdate is a task_update event: the conversation's most recently
// updated task changed.
type TaskUpdate struct {
	TaskID        string    `json:"task_id"`
	WorkspaceID   string    `json:"workspace_id"`
	Status        string    `json:"status"`
	ErrorClass    string    `json:"error_class"`
	FailureReason string    `json:"failure_reason"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// MessageAdded is a message_added event: fetch the conversation for the
// message's content.
type MessageAdded struct {
	MessageID string    `json:"message_id"`
	Role      string    `json:"role"`
	TaskID    string    `json:"task_id"`
	CreatedAt time.Time `json:"created_at"`
}

// DispatchUpdate is a dispatch_update event: a dispatch of the
// conversation changed status.
type DispatchUpdate struct {
	DispatchID string    `json:"dispatch_id"`
	Status     string    `json:"status"`
	Reply      string    `json:"reply"`
	Error      string    `json:"error"`
	ErrorClass string    `json:"error_class"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// StreamEvent is what Stream.Next returns.
type StreamEvent struct {
	// Type is EventConnected, EventDisconnected, a server event type
	// (EventTaskUpdate, …), or one this client doesn't know, from a later
	// server: ignore it.
	Type string

	Reconnected bool          // EventConnected
	Err         error         // EventDisconnected; or an event whose data didn't decode
	RetryIn     time.Duration // EventDisconnected

	Task     *TaskUpdate
	Message  *MessageAdded
	Dispatch *DispatchUpdate
	// Data is a server event's raw data.
	Data []byte
}

// ErrStreamIdle is why a stream that sent nothing, not even a heartbeat,
// for the idle timeout was dropped.
var ErrStreamIdle = errors.New("the stream went quiet (no heartbeat)")

// ErrStreamClosed is Next's error after Close.
var ErrStreamClosed = errors.New("the stream is closed")

// maxEvent caps one stream event.
const maxEvent = 1 << 20

// Stream is a conversation's event stream, GET
// /conversations/{id}/stream, kept open: when it drops, Next reconnects
// with backoff (1 s doubling to 15 s), and a connection that sends
// nothing for 45 s (three missed heartbeats) is dropped. The backoff
// starts over only after a connection has stayed up for 15 s, so a
// server or proxy that accepts the stream and drops it at once is
// retried no faster than one that refuses it. A 401 ends the stream:
// Next returns the error.
//
// Next is called from one goroutine at a time; Close from any.
type Stream struct {
	c    *Client
	conv string

	minBackoff, maxBackoff, idle time.Duration
	// stable is how long a connection stays up before its drop starts
	// the backoff over.
	stable time.Duration

	mu     sync.Mutex
	closed bool
	done   chan struct{}           // closed by Close
	cancel context.CancelCauseFunc // the open connection's

	// Owned by Next.
	events      chan connEvent // the open connection's, nil when none
	backoff     time.Duration
	wait        time.Duration // before the next connect
	connected   bool          // ever
	connectedAt time.Time     // the open connection's
	err         error         // terminal
}

// connEvent is one event, or the error that ended a connection.
type connEvent struct {
	ev  sseEvent
	err error
}

// OpenStream returns the stream of a conversation. It connects on the
// first Next. A stream may be opened before the conversation exists.
func (c *Client) OpenStream(conversationID string) *Stream {
	return &Stream{c: c, conv: conversationID, done: make(chan struct{}),
		minBackoff: time.Second, maxBackoff: 15 * time.Second, stable: 15 * time.Second, idle: 45 * time.Second}
}

// Next returns the next event: a server event, or EventConnected and
// EventDisconnected as the connection comes and goes. It waits until
// there is one or ctx ends; an ended ctx leaves the stream as it was.
func (s *Stream) Next(ctx context.Context) (StreamEvent, error) {
	if s.err != nil {
		return StreamEvent{}, s.err
	}
	if s.isClosed() {
		return StreamEvent{}, ErrStreamClosed
	}
	if s.events == nil {
		return s.connect(ctx)
	}
	select {
	case <-ctx.Done():
		return StreamEvent{}, ctx.Err()
	case <-s.done:
		return StreamEvent{}, ErrStreamClosed
	case ce := <-s.events:
		if ce.err != nil {
			s.drop()
			if s.isClosed() {
				return StreamEvent{}, ErrStreamClosed
			}
			if time.Since(s.connectedAt) >= s.stable {
				s.backoff = 0
			}
			return s.disconnected(ce.err), nil
		}
		return decodeEvent(ce.ev), nil
	}
}

// connect waits out the backoff and opens a connection.
func (s *Stream) connect(ctx context.Context) (StreamEvent, error) {
	if s.wait > 0 {
		t := time.NewTimer(s.wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return StreamEvent{}, ctx.Err()
		case <-s.done:
			t.Stop()
			return StreamEvent{}, ErrStreamClosed
		case <-t.C:
		}
		s.wait = 0
	}
	connCtx, cancel := context.WithCancelCause(context.Background())
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel(ErrStreamClosed)
		return StreamEvent{}, ErrStreamClosed
	}
	s.cancel = cancel
	s.mu.Unlock()

	resp, err := s.open(ctx, connCtx, cancel)
	if err != nil {
		s.drop()
		if s.isClosed() {
			return StreamEvent{}, ErrStreamClosed
		}
		if ctx.Err() != nil {
			return StreamEvent{}, ctx.Err()
		}
		if Unauthorized(err) {
			s.err = err
			return StreamEvent{}, err
		}
		return s.disconnected(err), nil
	}
	events := make(chan connEvent, 1)
	s.events = events
	go s.read(connCtx, cancel, resp.Body, events)
	reconnected := s.connected
	s.connected, s.connectedAt = true, time.Now()
	return StreamEvent{Type: EventConnected, Reconnected: reconnected}, nil
}

// open sends the stream's request on connCtx, giving up when ctx ends
// first.
func (s *Stream) open(ctx, connCtx context.Context, cancel context.CancelCauseFunc) (*http.Response, error) {
	stop := context.AfterFunc(ctx, func() { cancel(ctx.Err()) })
	defer stop()
	req, err := s.c.newRequest(connCtx, request{method: http.MethodGet, path: "/conversations/" + seg(s.conv) + "/stream",
		header: http.Header{"Accept": {"text/event-stream"}, "Cache-Control": {"no-cache"}}})
	if err != nil {
		return nil, err
	}
	resp, err := s.c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	mt := resp.Header.Get("Content-Type")
	if resp.StatusCode == http.StatusOK && strings.HasPrefix(mt, "text/event-stream") {
		return resp, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil, intercepted(resp)
	}
	return nil, decode(resp, nil, nil)
}

// read sends the connection's events to events until it ends, and then
// the error that ended it. It stops early only when the stream is closed.
// The idle timer doesn't run while an event waits for Next: a slow reader
// isn't a quiet server.
func (s *Stream) read(ctx context.Context, cancel context.CancelCauseFunc, body io.ReadCloser, events chan<- connEvent) {
	defer body.Close()
	idle := time.AfterFunc(s.idle, func() { cancel(ErrStreamIdle) })
	defer idle.Stop()
	stop := context.AfterFunc(ctx, func() { body.Close() })
	defer stop()
	r := newSSEReader(body, maxEvent)
	r.onLine = func() { idle.Reset(s.idle) }
	for {
		ev, err := r.next()
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				err = cause
			} else if errors.Is(err, io.EOF) {
				err = errors.New("the server ended the stream")
			}
			select {
			case events <- connEvent{err: err}:
			case <-s.done:
			}
			return
		}
		idle.Stop()
		select {
		case events <- connEvent{ev: ev}:
		case <-s.done:
			return
		}
		idle.Reset(s.idle)
	}
}

// disconnected schedules the next connect and reports the drop.
func (s *Stream) disconnected(err error) StreamEvent {
	if s.backoff == 0 {
		s.backoff = s.minBackoff
	} else {
		s.backoff = min(2*s.backoff, s.maxBackoff)
	}
	s.wait = s.backoff
	return StreamEvent{Type: EventDisconnected, Err: err, RetryIn: s.backoff}
}

// drop ends the open connection, if any.
func (s *Stream) drop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel(nil)
	}
	s.events = nil
}

// Close ends the stream: the connection closes and Next returns
// ErrStreamClosed.
func (s *Stream) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel(ErrStreamClosed)
	}
	return nil
}

func (s *Stream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func decodeEvent(ev sseEvent) StreamEvent {
	out := StreamEvent{Type: ev.Type, Data: []byte(ev.Data)}
	var v any
	switch ev.Type {
	case EventTaskUpdate:
		out.Task = &TaskUpdate{}
		v = out.Task
	case EventMessageAdded:
		out.Message = &MessageAdded{}
		v = out.Message
	case EventDispatchUpdate:
		out.Dispatch = &DispatchUpdate{}
		v = out.Dispatch
	default:
		return out
	}
	if err := json.Unmarshal(out.Data, v); err != nil {
		out.Task, out.Message, out.Dispatch = nil, nil, nil
		out.Err = fmt.Errorf("a %s event: %w", ev.Type, err)
	}
	return out
}

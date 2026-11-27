package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSSEReader(t *testing.T) {
	in := ": heartbeat\n\n" +
		"event: task_update\ndata: {\"task_id\":\"t-1\"}\n\n" +
		"event: message_added\r\ndata: {\"a\":1}\r\ndata: {\"b\":2}\r\n\r\n" +
		"data: no event name\nid: 7\nretry: 100\nunknown: field\n\n" +
		"event:no-space\ndata:x\n\n" +
		"event: only-a-name\n\n" +
		"event: cut-off\ndata: never finished"
	r := newSSEReader(strings.NewReader(in), 1<<20)
	want := []sseEvent{
		{Type: "task_update", Data: `{"task_id":"t-1"}`},
		{Type: "message_added", Data: "{\"a\":1}\n{\"b\":2}"},
		{Type: "message", Data: "no event name"},
		{Type: "no-space", Data: "x"},
	}
	for i, w := range want {
		got, err := r.next()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if got.Type != w.Type || got.Data != w.Data {
			t.Errorf("event %d = %+v, want %+v", i, got, w)
		}
	}
	// An event with no data isn't dispatched, and one cut off by the end
	// of the stream is dropped.
	if got, err := r.next(); !errors.Is(err, io.EOF) {
		t.Errorf("after the last event: %+v, %v; want io.EOF", got, err)
	}
}

func TestSSEReaderCountsHeartbeats(t *testing.T) {
	var lines int
	r := newSSEReader(strings.NewReader(": heartbeat\n\n: heartbeat\n\nevent: x\ndata: 1\n\n"), 1<<20)
	r.onLine = func() { lines++ }
	if _, err := r.next(); err != nil {
		t.Fatal(err)
	}
	if lines < 4 {
		t.Errorf("onLine ran %d times, want every line (heartbeats too)", lines)
	}
}

func TestSSEReaderLimitsAnEvent(t *testing.T) {
	r := newSSEReader(strings.NewReader("event: big\ndata: "+strings.Repeat("x", 100)+"\n\n"), 64)
	if _, err := r.next(); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %v, want too large", err)
	}
}

// streamServer serves /api/v1/conversations/{id}/stream: each connection
// takes the next script from conns and writes it, then closes (or, with
// hold, stays open until the client goes).
type streamServer struct {
	mu    sync.Mutex
	conns []func(w http.ResponseWriter, f http.Flusher)
	n     int
}

func (s *streamServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/conversations/c-1/stream" {
			t.Errorf("stream path %q", r.URL.Path)
		}
		if a := r.Header.Get("Accept"); a != "text/event-stream" {
			t.Errorf("stream Accept = %q", a)
		}
		s.mu.Lock()
		i := s.n
		s.n++
		s.mu.Unlock()
		if i >= len(s.conns) {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f := w.(http.Flusher)
		f.Flush()
		s.conns[i](w, f)
	}
}

func write(w io.Writer, f http.Flusher, s string) {
	io.WriteString(w, s)
	f.Flush()
}

func nextEvent(t *testing.T, s *Stream) StreamEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ev, err := s.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return ev
}

func TestStreamEventsAndReconnect(t *testing.T) {
	srv := &streamServer{conns: []func(http.ResponseWriter, http.Flusher){
		func(w http.ResponseWriter, f http.Flusher) {
			write(w, f, ": heartbeat\n\n")
			write(w, f, "event: task_update\ndata: {\"task_id\":\"t-1\",\"workspace_id\":\"w-1\",\"status\":\"running\",\"updated_at\":\"2026-11-20T10:00:00Z\"}\n\n")
			write(w, f, "event: message_added\ndata: {\"message_id\":\"m-1\",\"role\":\"assistant\",\"task_id\":\"t-1\",\"created_at\":\"2026-11-20T10:00:01Z\"}\n\n")
			write(w, f, "event: dispatch_update\ndata: {\"dispatch_id\":\"d-1\",\"status\":\"succeeded\",\"reply\":\"ok\",\"updated_at\":\"2026-11-20T10:00:02Z\"}\n\n")
			write(w, f, "event: task_hold\ndata: {\"task_id\":\"t-1\"}\n\n")
			// and the connection drops
		},
		func(w http.ResponseWriter, f http.Flusher) {
			write(w, f, "event: task_update\ndata: {\"task_id\":\"t-1\",\"status\":\"awaiting_input\",\"updated_at\":\"2026-11-20T10:00:03Z\"}\n\n")
		},
	}}
	c := newTest(t, "tok", srv.handler(t))
	s := c.OpenStream("c-1")
	s.minBackoff, s.maxBackoff = time.Millisecond, 4*time.Millisecond
	defer s.Close()

	if ev := nextEvent(t, s); ev.Type != EventConnected || ev.Reconnected {
		t.Fatalf("first event %+v, want connected", ev)
	}
	ev := nextEvent(t, s)
	if ev.Type != EventTaskUpdate || ev.Task == nil || ev.Task.TaskID != "t-1" || ev.Task.Status != TaskRunning || ev.Task.WorkspaceID != "w-1" || ev.Task.UpdatedAt.IsZero() {
		t.Errorf("task_update = %+v %+v", ev, ev.Task)
	}
	ev = nextEvent(t, s)
	if ev.Type != EventMessageAdded || ev.Message == nil || ev.Message.MessageID != "m-1" || ev.Message.Role != RoleAssistant || ev.Message.TaskID != "t-1" {
		t.Errorf("message_added = %+v %+v", ev, ev.Message)
	}
	ev = nextEvent(t, s)
	if ev.Type != EventDispatchUpdate || ev.Dispatch == nil || ev.Dispatch.DispatchID != "d-1" || ev.Dispatch.Status != DispatchSucceeded || ev.Dispatch.Reply != "ok" {
		t.Errorf("dispatch_update = %+v %+v", ev, ev.Dispatch)
	}
	// An event this client doesn't know (a later server's) is passed on
	// raw, for the caller to ignore.
	ev = nextEvent(t, s)
	if ev.Type != "task_hold" || string(ev.Data) != `{"task_id":"t-1"}` || ev.Err != nil {
		t.Errorf("unknown event = %+v", ev)
	}
	ev = nextEvent(t, s)
	if ev.Type != EventDisconnected || ev.Err == nil || ev.RetryIn != time.Millisecond {
		t.Errorf("after the drop: %+v, want disconnected, retrying in 1ms", ev)
	}
	// Reconnected: message_added isn't replayed, so the caller refetches
	// the conversation.
	if ev := nextEvent(t, s); ev.Type != EventConnected || !ev.Reconnected {
		t.Errorf("after the retry: %+v, want reconnected", ev)
	}
	if ev := nextEvent(t, s); ev.Type != EventTaskUpdate || ev.Task.Status != TaskAwaitingInput {
		t.Errorf("after reconnecting: %+v", ev)
	}
}

func TestStreamBacksOffAndDoubles(t *testing.T) {
	var mu sync.Mutex
	var n int
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		io.WriteString(w, `{"error":"restarting","code":"unavailable"}`)
	})
	s := c.OpenStream("c-1")
	s.minBackoff, s.maxBackoff = time.Millisecond, 4*time.Millisecond
	defer s.Close()
	var waits []time.Duration
	for range 5 {
		ev := nextEvent(t, s)
		if ev.Type != EventDisconnected || !Is(ev.Err, CodeUnavailable) {
			t.Fatalf("event %+v, want disconnected by the 503", ev)
		}
		waits = append(waits, ev.RetryIn)
	}
	if got := fmt.Sprint(waits); got != "[1ms 2ms 4ms 4ms 4ms]" {
		t.Errorf("waits = %s, want doubling to the cap", got)
	}
}

func TestStreamDefaultBackoffIsTheDesigns(t *testing.T) {
	s := (&Client{}).OpenStream("c")
	if s.minBackoff != time.Second || s.maxBackoff != 15*time.Second {
		t.Errorf("backoff %v..%v, want 1s doubling to 15s", s.minBackoff, s.maxBackoff)
	}
	if s.stable != 15*time.Second {
		t.Errorf("stable after %v, want the longest backoff", s.stable)
	}
	if s.idle != 45*time.Second {
		t.Errorf("idle timeout %v, want three missed 15s heartbeats", s.idle)
	}
}

// A 401 isn't retried: the token is no good, and the caller signs out.
func TestStreamStopsOnUnauthorized(t *testing.T) {
	var n int
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		io.WriteString(w, `{"error":"unauthorized","code":"unauthorized"}`)
	})
	s := c.OpenStream("c-1")
	s.minBackoff = time.Millisecond
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Next(ctx); !Unauthorized(err) {
		t.Fatalf("err = %v, want the 401", err)
	}
	if _, err := s.Next(ctx); !Unauthorized(err) {
		t.Errorf("after a 401: %v, want the 401 again", err)
	}
	if n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

// A stream that goes quiet (not even a heartbeat) is a dead connection:
// it's dropped and reconnected.
func TestStreamIdleTimeout(t *testing.T) {
	srv := &streamServer{conns: []func(http.ResponseWriter, http.Flusher){
		func(w http.ResponseWriter, f http.Flusher) { time.Sleep(time.Second) },
		func(w http.ResponseWriter, f http.Flusher) {
			write(w, f, "event: task_update\ndata: {\"task_id\":\"t-2\",\"status\":\"running\"}\n\n")
		},
	}}
	c := newTest(t, "tok", srv.handler(t))
	s := c.OpenStream("c-1")
	s.minBackoff, s.idle = time.Millisecond, 50*time.Millisecond
	defer s.Close()
	if ev := nextEvent(t, s); ev.Type != EventConnected {
		t.Fatalf("%+v", ev)
	}
	start := time.Now()
	ev := nextEvent(t, s)
	if ev.Type != EventDisconnected || !errors.Is(ev.Err, ErrStreamIdle) {
		t.Fatalf("%+v, want disconnected as idle", ev)
	}
	if time.Since(start) > 900*time.Millisecond {
		t.Errorf("idle drop took %v", time.Since(start))
	}
	if ev := nextEvent(t, s); ev.Type != EventConnected || !ev.Reconnected {
		t.Fatalf("%+v", ev)
	}
	if ev := nextEvent(t, s); ev.Type != EventTaskUpdate || ev.Task.TaskID != "t-2" {
		t.Errorf("%+v", ev)
	}
}

func TestStreamNextHonoursTheContext(t *testing.T) {
	srv := &streamServer{conns: []func(http.ResponseWriter, http.Flusher){
		func(w http.ResponseWriter, f http.Flusher) { time.Sleep(2 * time.Second) },
	}}
	c := newTest(t, "tok", srv.handler(t))
	s := c.OpenStream("c-1")
	defer s.Close()
	if ev := nextEvent(t, s); ev.Type != EventConnected {
		t.Fatalf("%+v", ev)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := s.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("Next took %v after its context ended", time.Since(start))
	}
}

// A server (or proxy) that accepts the stream and drops it at once gets
// the same doubling backoff as one that refuses it: the backoff resets
// only after a connection has stayed up.
func TestStreamBackoffSurvivesShortConnections(t *testing.T) {
	drop := func(http.ResponseWriter, http.Flusher) {}
	srv := &streamServer{conns: []func(http.ResponseWriter, http.Flusher){drop, drop, drop, drop, drop}}
	c := newTest(t, "tok", srv.handler(t))
	s := c.OpenStream("c-1")
	s.minBackoff, s.maxBackoff = time.Millisecond, 4*time.Millisecond
	s.stable = time.Hour
	defer s.Close()
	var waits []time.Duration
	for len(waits) < 4 {
		ev := nextEvent(t, s)
		if ev.Type == EventDisconnected {
			waits = append(waits, ev.RetryIn)
		}
	}
	if got := fmt.Sprint(waits); got != "[1ms 2ms 4ms 4ms]" {
		t.Errorf("waits = %s, want doubling across short connections", got)
	}
}

// A connection that stayed up resets the backoff: its drop is retried
// after the shortest wait.
func TestStreamBackoffResetsAfterAStableConnection(t *testing.T) {
	srv := &streamServer{conns: []func(http.ResponseWriter, http.Flusher){
		func(http.ResponseWriter, http.Flusher) {},
		func(http.ResponseWriter, http.Flusher) {},
		func(w http.ResponseWriter, f http.Flusher) { time.Sleep(30 * time.Millisecond) },
	}}
	c := newTest(t, "tok", srv.handler(t))
	s := c.OpenStream("c-1")
	s.minBackoff, s.maxBackoff, s.stable = time.Millisecond, time.Second, 20*time.Millisecond
	defer s.Close()
	var waits []time.Duration
	for len(waits) < 3 {
		ev := nextEvent(t, s)
		if ev.Type == EventDisconnected {
			waits = append(waits, ev.RetryIn)
		}
	}
	if got := fmt.Sprint(waits); got != "[1ms 2ms 1ms]" {
		t.Errorf("waits = %s, want a reset after the stable connection", got)
	}
}

func TestStreamCloseEndsTheBackoffWait(t *testing.T) {
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		io.WriteString(w, `{"error":"restarting"}`)
	})
	s := c.OpenStream("c-1")
	s.minBackoff = time.Hour
	if ev := nextEvent(t, s); ev.Type != EventDisconnected {
		t.Fatalf("%+v", ev)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.Close()
	}()
	start := time.Now()
	if _, err := s.Next(context.Background()); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("err = %v, want ErrStreamClosed", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("Next took %v after Close", time.Since(start))
	}
}

func TestStreamCloseDuringConnect(t *testing.T) {
	var s *Stream
	c := newTest(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		s.Close()
		<-r.Context().Done()
	})
	s = c.OpenStream("c-1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ev, err := s.Next(ctx); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("Next = %+v, %v; want ErrStreamClosed", ev, err)
	}
}

// holdingTransport hands its answer to the stream only when the test lets
// it: the 401 has been received before the stream is closed.
type holdingTransport struct {
	entered chan struct{}
	answer  chan struct{}
}

func (h *holdingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	close(h.entered)
	<-h.answer
	return &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized","code":"unauthorized"}`)),
	}, nil
}

// A 401 the stream had already received when it was closed is still
// Next's answer: closing it doesn't hide that the server refused the
// token.
func TestStreamCloseKeepsAReceived401(t *testing.T) {
	c, err := New("https://loomux.example.net", "tok")
	if err != nil {
		t.Fatal(err)
	}
	rt := &holdingTransport{entered: make(chan struct{}), answer: make(chan struct{})}
	c.hc.Transport = rt
	s := c.OpenStream("c-1")
	next := make(chan error, 1)
	go func() {
		_, err := s.Next(context.Background())
		next <- err
	}()
	<-rt.entered
	s.Close()
	close(rt.answer)
	if err := <-next; !Unauthorized(err) {
		t.Errorf("Next after Close = %v, want the 401 it had received", err)
	}
}

func TestStreamCloseEndsNext(t *testing.T) {
	c := newTest(t, "tok", (&streamServer{}).handler(t))
	s := c.OpenStream("c-1")
	s.Close()
	if _, err := s.Next(context.Background()); !errors.Is(err, ErrStreamClosed) {
		t.Errorf("err = %v, want ErrStreamClosed", err)
	}
}

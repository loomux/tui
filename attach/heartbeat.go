package attach

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/Loomux/tui/client"
)

// heartbeat renews a session's hold in its own goroutine, owned by the
// attach routine: it runs while the attach command has the terminal, and
// in the TUI while the screens are suspended (the design's "Suspending
// and resuming the TUI").
type heartbeat struct {
	cancel context.CancelFunc
	done   chan struct{}
	// gone is closed when the server has said the hold is gone for good.
	gone chan struct{}

	mu sync.Mutex
	// goneWhy is that answer.
	goneWhy error
	// expires is when the lease runs out by this machine's clock: the
	// lease's length after the last renewal.
	expires time.Time
}

// startHeartbeat starts renewing the hold: every s.every; after a failed
// one every s.retry until the lease has run out, then every s.every
// again. It never gives up on a server that doesn't answer: holds
// outlive a restart or deploy, and "a terminal still attached resumes
// heartbeats once the API answers" (the design's "Expiry, crashes and
// restarts"). It ends when the session is released, or when the server
// answers that the hold is gone: ended (410), or not this session's to
// renew any more (401, 403, 404).
func (s *Session) startHeartbeat() {
	ctx, cancel := context.WithCancel(context.Background())
	hb := &heartbeat{cancel: cancel, done: make(chan struct{}), gone: make(chan struct{}), expires: time.Now().Add(s.ttl)}
	s.beat = hb
	go func() {
		defer close(hb.done)
		failed := false
		for {
			wait := time.NewTimer(s.beatDelay(failed, time.Now(), hb.expiry(time.Time{})))
			select {
			case <-ctx.Done():
				wait.Stop()
				return
			case <-wait.C:
			}
			rctx, rcancel := context.WithTimeout(ctx, s.beatTimeout)
			hold, err := s.api.Heartbeat(rctx, s.TaskID, s.Hold.ID)
			rcancel()
			switch {
			case ctx.Err() != nil:
				return // released meanwhile: the answer means nothing
			case err == nil:
				failed = false
				ttl := s.ttl
				if hold != nil && hold.TTLSeconds > 0 {
					ttl = time.Duration(hold.TTLSeconds) * time.Second
				}
				hb.mu.Lock()
				hb.expires = time.Now().Add(ttl)
				hb.mu.Unlock()
			case holdGone(err):
				hb.mu.Lock()
				hb.goneWhy = err
				hb.mu.Unlock()
				close(hb.gone)
				return
			default:
				failed = true
			}
		}
	}()
}

// beatDelay is how long to wait before the next heartbeat.
func (s *Session) beatDelay(failed bool, now, expires time.Time) time.Duration {
	if failed && now.Before(expires) {
		return min(s.retry, s.every)
	}
	return s.every
}

// holdGone reports whether a heartbeat's error is the server saying the
// hold is gone for good, as opposed to a heartbeat that didn't get
// through.
func holdGone(err error) bool {
	var e *client.Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Status {
	case http.StatusGone, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

// stop ends the heartbeat and waits for it. A session whose heartbeat
// never started has none to stop.
func (hb *heartbeat) stop() {
	if hb == nil {
		return
	}
	hb.cancel()
	<-hb.done
}

// goneErr is the server's answer that the hold is gone, or nil.
func (hb *heartbeat) goneErr() error {
	if hb == nil {
		return nil
	}
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return hb.goneWhy
}

// expiry is when the lease runs out by this machine's clock, or fallback
// for a session whose heartbeat never started.
func (hb *heartbeat) expiry(fallback time.Time) time.Time {
	if hb == nil {
		return fallback
	}
	hb.mu.Lock()
	defer hb.mu.Unlock()
	return hb.expires
}

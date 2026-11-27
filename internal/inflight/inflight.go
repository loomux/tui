// Package inflight is the screens' account of the requests they have
// asked for: how many are still to answer, and the API 401s they answered
// with that no handler has applied yet. A root whose program stops before
// the answers are handled waits for them, at most a short while, and then
// applies the 401 rule to what came: the token the server refused is
// still the stored one.
package inflight

import (
	"errors"
	"slices"
	"sync"
	"time"
)

// Group counts requests in flight. A request is counted where its command
// is made, so that a root that stops sees it from the moment it exists,
// and answered when the command returns. One the stopped program never
// ran keeps its count: Wait is bounded and gives up on it.
type Group struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // closed when n falls to zero; a new one is made when it rises from zero
}

// Start counts a request.
func (g *Group) Start() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == 0 {
		g.idle = make(chan struct{})
	}
	g.n++
}

// Done drops a request that has answered.
func (g *Group) Done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n--
	if g.n == 0 {
		close(g.idle)
	}
}

// Wait waits for every counted request to answer, d at most. It reports
// whether they all did.
func (g *Group) Wait(d time.Duration) bool {
	g.mu.Lock()
	if g.n == 0 {
		g.mu.Unlock()
		return true
	}
	idle := g.idle
	g.mu.Unlock()
	if d <= 0 {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-idle:
		return true
	case <-t.C:
		return false
	}
}

// Errors keeps errors until they have been said, or, if the screens end
// first, until Take returns them. Each is said once.
type Errors struct {
	mu   sync.Mutex
	errs []error
}

// Put keeps err until it is said.
func (e *Errors) Put(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.errs = append(e.errs, err)
}

// Said drops err: a handler has it.
func (e *Errors) Said(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.errs = slices.DeleteFunc(e.errs, func(k error) bool { return k == err })
}

// Take returns every error not yet said, and drops them.
func (e *Errors) Take() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	errs := make([]error, len(e.errs))
	for i, err := range e.errs {
		errs[i] = err
	}
	e.errs = nil
	return errors.Join(errs...)
}

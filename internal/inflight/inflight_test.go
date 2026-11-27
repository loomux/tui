package inflight

import (
	"errors"
	"testing"
	"time"
)

// A group reports the requests that answer, and gives up on one that
// never does after the wait.
func TestWaitIsBounded(t *testing.T) {
	var g Group
	if !g.Wait(0) {
		t.Error("an empty group didn't report it was idle")
	}
	g.Start()
	g.Start()
	if g.Wait(10 * time.Millisecond) {
		t.Error("the group reported idle with two requests in flight")
	}
	g.Done()
	g.Done()
	if !g.Wait(time.Second) {
		t.Error("the group didn't report idle once both answered")
	}
	// A command the program never ran keeps its count: the wait gives up
	// rather than block.
	g.Start()
	start := time.Now()
	if g.Wait(20 * time.Millisecond) {
		t.Error("the group reported idle with a request that never ran")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("the bounded wait took %v", d)
	}
	g.Done()
}

// A group's wait begins again once a new request starts.
func TestWaitAfterIdle(t *testing.T) {
	var g Group
	g.Start()
	g.Done()
	g.Start()
	if g.Wait(10 * time.Millisecond) {
		t.Error("the group reported idle with the second request in flight")
	}
	g.Done()
	if !g.Wait(time.Second) {
		t.Error("the second answer didn't make the group idle")
	}
}

// Errors says each kept error once, and Take returns the rest.
func TestErrorsSaidOnce(t *testing.T) {
	var e Errors
	first, second := errors.New("first"), errors.New("second")
	e.Put(first)
	e.Put(second)
	e.Put(nil)
	e.Said(first)
	if got := e.Take(); !errors.Is(got, second) || errors.Is(got, first) {
		t.Errorf("Take = %v, want only the unsaid error", got)
	}
	if got := e.Take(); got != nil {
		t.Errorf("Take after Take = %v, want nil", got)
	}
}

// Take joins the errors not yet said, and drops them.
func TestTakeJoins(t *testing.T) {
	var e Errors
	one, two := errors.New("one"), errors.New("two")
	e.Put(one)
	e.Put(two)
	got := e.Take()
	if !errors.Is(got, one) || !errors.Is(got, two) {
		t.Errorf("Take = %v, want both errors", got)
	}
	if got := e.Take(); got != nil {
		t.Errorf("the taken errors are still kept: %v", got)
	}
}

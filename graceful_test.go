package graceful

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	s := New()
	if s.timeout != 30*time.Second {
		t.Fatalf("expected default timeout 30s, got %v", s.timeout)
	}
	if len(s.signals) != 2 {
		t.Fatalf("expected 2 default signals, got %d", len(s.signals))
	}
}

func TestRegister(t *testing.T) {
	s := New()
	s.Register("db", PriorityNormal, func(ctx context.Context) error { return nil })
	s.Register("cache", PriorityFirst, func(ctx context.Context) error { return nil })

	if len(s.hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(s.hooks))
	}
	if s.hooks[0].name != "db" {
		t.Fatalf("expected first registered hook name 'db', got %q", s.hooks[0].name)
	}
	if s.hooks[1].name != "cache" {
		t.Fatalf("expected second registered hook name 'cache', got %q", s.hooks[1].name)
	}
}

func TestTrigger(t *testing.T) {
	s := New()
	var called atomic.Bool

	s.Register("hook", PriorityNormal, func(ctx context.Context) error {
		called.Store(true)
		return nil
	})

	go s.Wait()
	s.Trigger()

	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}

	if !called.Load() {
		t.Fatal("expected hook to be called")
	}
}

func TestPriorityOrder(t *testing.T) {
	s := New()
	var order []string
	var mu sync.Mutex

	record := func(name string) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	s.Register("last", PriorityLast, record("last"))
	s.Register("first", PriorityFirst, record("first"))
	s.Register("normal", PriorityNormal, record("normal"))

	go s.Wait()
	s.Trigger()

	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}

	if len(order) != 3 {
		t.Fatalf("expected 3 hooks, got %d", len(order))
	}
	if order[0] != "first" {
		t.Fatalf("expected first hook to run first, got %q", order[0])
	}
	if order[1] != "normal" {
		t.Fatalf("expected normal hook to run second, got %q", order[1])
	}
	if order[2] != "last" {
		t.Fatalf("expected last hook to run third, got %q", order[2])
	}
}

func TestParallelWithinPriority(t *testing.T) {
	s := New()
	var started sync.WaitGroup
	started.Add(2)
	barrier := make(chan struct{})

	makeHook := func() func(ctx context.Context) error {
		return func(ctx context.Context) error {
			started.Done()
			<-barrier
			return nil
		}
	}

	s.Register("a", PriorityNormal, makeHook())
	s.Register("b", PriorityNormal, makeHook())

	go s.Wait()
	s.Trigger()

	// Both hooks should start concurrently. Wait for both to signal start.
	done := make(chan struct{})
	go func() {
		started.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Both hooks started in parallel — success.
	case <-time.After(2 * time.Second):
		t.Fatal("hooks did not start in parallel")
	}

	close(barrier)

	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}
}

func TestTimeout(t *testing.T) {
	s := New(WithTimeout(100 * time.Millisecond))

	s.Register("slow", PriorityNormal, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	go s.Wait()
	s.Trigger()

	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}

	err := s.Err()
	if err == nil {
		t.Fatal("expected error from timed out hook")
	}
}

func TestHookTimeout(t *testing.T) {
	s := New(WithTimeout(5 * time.Second))

	var hookErr atomic.Value

	s.RegisterTimeout("fast", PriorityNormal, 100*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		hookErr.Store(ctx.Err())
		return ctx.Err()
	})

	go s.Wait()
	s.Trigger()

	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}

	err := s.Err()
	if err == nil {
		t.Fatal("expected error from hook with per-hook timeout")
	}

	stored := hookErr.Load()
	if stored == nil {
		t.Fatal("expected hook context to be cancelled")
	}
	if !errors.Is(stored.(error), context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", stored)
	}
}

func TestDone(t *testing.T) {
	s := New()

	s.Register("hook", PriorityNormal, func(ctx context.Context) error {
		return nil
	})

	go s.Wait()

	// Done should not be closed yet.
	select {
	case <-s.Done():
		t.Fatal("Done should not be closed before trigger")
	default:
	}

	s.Trigger()

	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Done")
	}
}

func TestErr(t *testing.T) {
	s := New()

	errDB := errors.New("db close failed")
	errCache := errors.New("cache flush failed")

	s.Register("db", PriorityNormal, func(ctx context.Context) error {
		return errDB
	})
	s.Register("cache", PriorityNormal, func(ctx context.Context) error {
		return errCache
	})

	go s.Wait()
	s.Trigger()

	err := s.Err()
	if err == nil {
		t.Fatal("expected combined error")
	}

	errStr := err.Error()
	if !containsSubstring(errStr, "db close failed") {
		t.Fatalf("expected error to contain 'db close failed', got %q", errStr)
	}
	if !containsSubstring(errStr, "cache flush failed") {
		t.Fatalf("expected error to contain 'cache flush failed', got %q", errStr)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && searchSubstring(s, sub)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Package graceful provides a shutdown orchestrator for Go services.
//
// It allows registering shutdown hooks with priority ordering. Hooks within
// the same priority level run in parallel, while priority groups execute
// sequentially from lowest (first) to highest (last). A global timeout and
// per-hook timeouts ensure shutdown completes in bounded time.
package graceful

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"
)

// Priority determines the order in which shutdown hooks execute.
// Lower values run first. Hooks sharing the same priority run in parallel.
type Priority int

const (
	// PriorityFirst runs before all other priority levels.
	PriorityFirst Priority = 0

	// PriorityNormal is the default priority level.
	PriorityNormal Priority = 50

	// PriorityLast runs after all other priority levels.
	PriorityLast Priority = 100
)

// Option configures the Shutdown orchestrator.
type Option func(*Shutdown)

// WithTimeout sets the global shutdown timeout. If the entire shutdown
// sequence exceeds this duration, remaining hooks are cancelled.
// The default is 30 seconds.
func WithTimeout(d time.Duration) Option {
	return func(s *Shutdown) {
		s.timeout = d
	}
}

// WithSignals sets the OS signals that trigger shutdown.
// The default signals are SIGTERM and SIGINT.
func WithSignals(signals ...os.Signal) Option {
	return func(s *Shutdown) {
		s.signals = signals
	}
}

type hook struct {
	name     string
	priority Priority
	timeout  time.Duration
	fn       func(ctx context.Context) error
}

// Shutdown orchestrates the graceful shutdown of a service by running
// registered hooks in priority order.
type Shutdown struct {
	timeout  time.Duration
	signals  []os.Signal
	hooks    []hook
	mu       sync.Mutex
	done     chan struct{}
	err      error
	once     sync.Once
	trigger  chan struct{}
}

// New creates a new Shutdown orchestrator with the given options.
func New(opts ...Option) *Shutdown {
	s := &Shutdown{
		timeout: 30 * time.Second,
		signals: []os.Signal{syscall.SIGTERM, syscall.SIGINT},
		done:    make(chan struct{}),
		trigger: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Register adds a shutdown hook with the given name, priority, and function.
// The function receives a context that is cancelled when the global timeout
// expires. Hooks with lower priority values run first. Hooks sharing the
// same priority run concurrently.
func (s *Shutdown) Register(name string, priority Priority, fn func(ctx context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, hook{
		name:     name,
		priority: priority,
		fn:       fn,
	})
}

// RegisterTimeout adds a shutdown hook with a per-hook timeout. If the hook
// does not complete within the specified duration, its context is cancelled
// independently of the global timeout.
func (s *Shutdown) RegisterTimeout(name string, priority Priority, timeout time.Duration, fn func(ctx context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, hook{
		name:     name,
		priority: priority,
		timeout:  timeout,
		fn:       fn,
	})
}

// Wait blocks until a registered OS signal is received or Trigger is called,
// then executes all hooks in priority order. Hooks within the same priority
// group run in parallel. Wait returns after all hooks complete or the global
// timeout expires.
func (s *Shutdown) Wait() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, s.signals...)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
	case <-s.trigger:
	}

	s.runHooks()
}

// Trigger programmatically initiates the shutdown sequence without waiting
// for an OS signal. It is safe to call multiple times; only the first call
// has any effect.
func (s *Shutdown) Trigger() {
	s.once.Do(func() {
		close(s.trigger)
	})
}

// Done returns a channel that is closed after the shutdown sequence completes.
func (s *Shutdown) Done() <-chan struct{} {
	return s.done
}

// Err returns the combined errors from all hooks that failed during shutdown.
// It returns nil if no hooks reported errors. Err should be called after
// the Done channel is closed.
func (s *Shutdown) Err() error {
	<-s.done
	return s.err
}

func (s *Shutdown) runHooks() {
	defer close(s.done)

	s.mu.Lock()
	hooks := make([]hook, len(s.hooks))
	copy(hooks, s.hooks)
	s.mu.Unlock()

	if len(hooks) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	// Group hooks by priority.
	groups := make(map[Priority][]hook)
	for _, h := range hooks {
		groups[h.priority] = append(groups[h.priority], h)
	}

	// Sort priority levels ascending.
	priorities := make([]Priority, 0, len(groups))
	for p := range groups {
		priorities = append(priorities, p)
	}
	sort.Slice(priorities, func(i, j int) bool {
		return priorities[i] < priorities[j]
	})

	var allErrs []error

	for _, p := range priorities {
		group := groups[p]
		errs := s.runGroup(ctx, group)
		allErrs = append(allErrs, errs...)

		// If the global context is done, stop processing further groups.
		if ctx.Err() != nil {
			allErrs = append(allErrs, fmt.Errorf("global timeout exceeded"))
			break
		}
	}

	s.err = errors.Join(allErrs...)
}

func (s *Shutdown) runGroup(ctx context.Context, group []hook) []error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	for _, h := range group {
		wg.Add(1)
		go func(h hook) {
			defer wg.Done()

			hookCtx := ctx
			var hookCancel context.CancelFunc

			if h.timeout > 0 {
				hookCtx, hookCancel = context.WithTimeout(ctx, h.timeout)
				defer hookCancel()
			}

			if err := h.fn(hookCtx); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("hook %q: %w", h.name, err))
				mu.Unlock()
			}
		}(h)
	}

	wg.Wait()
	return errs
}

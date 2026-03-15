# go-graceful

Graceful shutdown orchestrator for Go services. Register hooks with priorities, drain in order.

## Installation

```bash
go get github.com/philiprehberger/go-graceful
```

## Usage

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/philiprehberger/go-graceful"
)

func main() {
	s := graceful.New()

	s.Register("http-server", graceful.PriorityFirst, func(ctx context.Context) error {
		fmt.Println("stopping HTTP server")
		// server.Shutdown(ctx)
		return nil
	})

	s.Register("database", graceful.PriorityNormal, func(ctx context.Context) error {
		fmt.Println("closing database connections")
		// db.Close()
		return nil
	})

	s.Register("logger", graceful.PriorityLast, func(ctx context.Context) error {
		fmt.Println("flushing logs")
		// logger.Sync()
		return nil
	})

	s.Wait()

	if err := s.Err(); err != nil {
		log.Fatalf("shutdown errors: %v", err)
	}
}
```

### Priority Ordering

Hooks execute in priority order, from lowest value to highest:

- `PriorityFirst` (0) — runs first (e.g., stop accepting new requests)
- `PriorityNormal` (50) — default level (e.g., close database connections)
- `PriorityLast` (100) — runs last (e.g., flush logs)

Hooks within the same priority level run in parallel. The next priority group starts only after all hooks in the current group complete.

### Programmatic Trigger

Use `Trigger()` to initiate shutdown without waiting for an OS signal:

```go
s := graceful.New()

s.Register("cleanup", graceful.PriorityNormal, func(ctx context.Context) error {
	// cleanup logic
	return nil
})

go s.Wait()

// Trigger shutdown programmatically
s.Trigger()

<-s.Done()
```

## API

| Function / Type | Description |
|-----------------|-------------|
| `New(opts ...Option) *Shutdown` | Create a new shutdown orchestrator |
| `WithTimeout(d time.Duration) Option` | Set global shutdown timeout (default 30s) |
| `WithSignals(signals ...os.Signal) Option` | Set signals to listen for (default SIGTERM, SIGINT) |
| `Register(name, priority, fn)` | Register a shutdown hook |
| `RegisterTimeout(name, priority, timeout, fn)` | Register a hook with per-hook timeout |
| `Wait()` | Block until signal or trigger, then run hooks |
| `Trigger()` | Programmatically initiate shutdown |
| `Done() <-chan struct{}` | Channel closed when shutdown completes |
| `Err() error` | Combined errors from hooks |
| `PriorityFirst` | Priority 0 — runs first |
| `PriorityNormal` | Priority 50 — default |
| `PriorityLast` | Priority 100 — runs last |

## License

MIT

package loop

import (
	"context"
	"sync"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time check: a Group is a lifecycle component.
var _ interfaces.Lifecycle = (*Group)(nil)

// Runner is one background loop: Run blocks until ctx ends or the loop fails for good.
type Runner interface {
	// Run executes the loop until ctx ends (returning nil) or it cannot continue
	// (returning the error).
	Run(ctx context.Context) error
}

// RunnerFunc adapts a function into a Runner.
type RunnerFunc func(ctx context.Context) error

// Run calls f.
func (f RunnerFunc) Run(ctx context.Context) error { return f(ctx) }

// Group starts a service's loops together and waits for all of them.
type Group struct {
	// logger records a loop that stopped with an error.
	logger interfaces.Logger
	// cancel ends the context the runners were started with; set by Start.
	cancel context.CancelFunc
	// done is closed once every runner has returned; set by Start.
	done chan struct{}
	// runners are the loops in the order they were added.
	runners []namedRunner
	// mu guards cancel, done and runners.
	mu sync.Mutex
}

// namedRunner pairs a Runner with the name it is logged under.
type namedRunner struct {
	// runner is the loop.
	runner Runner
	// name labels the loop in logs.
	name string
}

// NewGroup returns an empty Group that logs to logger. A nil logger panics.
func NewGroup(logger interfaces.Logger) *Group {
	if logger == nil {
		panic("loop: logger must not be nil")
	}
	return &Group{logger: logger}
}

// Add registers r under name. Add after Start panics, as a wiring bug.
func (g *Group) Add(name string, r Runner) {
	if r == nil {
		panic("loop: runner must not be nil")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done != nil {
		panic("loop: Add after Start")
	}
	g.runners = append(g.runners, namedRunner{runner: r, name: name})
}

// Start runs every runner on its own goroutine under a context derived from ctx and
// returns at once. A runner that returns an error is logged at error with its name;
// the others keep running. A second Start is CodeConflict.
func (g *Group) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done != nil {
		return apperr.New(apperr.CodeConflict, "loop: group already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	g.cancel, g.done = cancel, make(chan struct{})
	var wg sync.WaitGroup
	for _, nr := range g.runners {
		wg.Go(func() {
			if err := nr.runner.Run(runCtx); err != nil {
				g.logger.Error("loop stopped", "loop", nr.name, "error", err)
			}
		})
	}
	go func() {
		wg.Wait()
		close(g.done)
	}()
	return nil
}

// Wait blocks until every runner has returned. It returns at once on a group that was
// never started.
func (g *Group) Wait() {
	g.mu.Lock()
	done := g.done
	g.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Stop cancels the runners and waits for them to return. If ctx ends first, Stop
// returns its error coded CodeTimeout or CodeCanceled. Stop on a group that was never
// started returns nil.
func (g *Group) Stop(ctx context.Context) error {
	g.mu.Lock()
	cancel, done := g.cancel, g.done
	g.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return apperr.Wrap(ctx.Err(), apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
			"loop: stop ended before every loop returned")
	}
}

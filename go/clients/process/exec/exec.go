// Package exec is the Supervisor that runs real operating-system processes.
package exec

import (
	"context"
	"io"
	"os"
	osexec "os/exec"
	"slices"
	"sync"
	"syscall"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

const (
	// firstBackoff is the wait before the second readiness check.
	firstBackoff = 50 * time.Millisecond

	// maxBackoff caps the wait between readiness checks.
	maxBackoff = 2 * time.Second

	// waitDelay bounds how long output copying outlives the process.
	waitDelay = time.Second
)

// Supervisor starts and stops processes.
type Supervisor struct {
	// started are the supervised handles, oldest first.
	started []*handle

	// mu guards started.
	mu sync.Mutex
}

// Compile-time assertion that Supervisor satisfies the contract.
var _ interfaces.Supervisor = (*Supervisor)(nil)

// New returns a Supervisor with no processes.
func New() *Supervisor { return &Supervisor{} }

// command builds spec's command.
func command(spec types.ProcessSpec) (*osexec.Cmd, error) {
	if len(spec.Args) == 0 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"process "+spec.Name+": no program",
		)
	}
	// The program is the caller's by design, and a supervised process outlives the
	// context that started it (Stop ends it), so it is not bound to one.
	prog, args := spec.Args[0], spec.Args[1:]
	cmd := osexec.Command(prog, args...) //nolint:noctx // see above
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Stdout, cmd.Stderr = orDiscard(spec.Stdout), orDiscard(spec.Stderr)
	// A child the process leaves behind may hold its output open; stop copying a
	// moment after the process itself exits.
	cmd.WaitDelay = waitDelay
	return cmd, nil
}

// orDiscard is w, or io.Discard when w is nil.
func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// Start starts spec and waits for ready.
func (s *Supervisor) Start(
	ctx context.Context,
	spec types.ProcessSpec,
	ready interfaces.Probe[interfaces.ProcessHandle],
) (interfaces.ProcessHandle, error) {
	cmd, err := command(spec)
	if err != nil {
		return nil, err
	}
	// Its own process group, so Stop reaches the children it starts.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeUnavailable, "start "+spec.Name)
	}
	h := newHandle(spec.Name, cmd)
	s.mu.Lock()
	s.started = append(s.started, h)
	s.mu.Unlock()
	if ready == nil {
		return h, nil
	}
	if err := h.awaitReady(ctx, ready); err != nil {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), maxBackoff)
		defer cancel()
		_ = h.Stop(stopCtx)
		return nil, err
	}
	return h, nil
}

// Detach starts spec and releases it.
func (s *Supervisor) Detach(spec types.ProcessSpec) error {
	cmd, err := command(spec)
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "start "+spec.Name)
	}
	if err := cmd.Process.Release(); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "release "+spec.Name)
	}
	return nil
}

// StopAll stops every supervised process, newest first, and returns the first error.
func (s *Supervisor) StopAll(ctx context.Context) error {
	s.mu.Lock()
	hs := slices.Clone(s.started)
	s.started = nil
	s.mu.Unlock()
	var first error
	for _, h := range slices.Backward(hs) {
		if err := h.Stop(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// handle is one started process.
type handle struct {
	// cmd is the running command.
	cmd *osexec.Cmd

	// err is the exit error, set before done closes.
	err error

	// done closes when the process exits.
	done chan struct{}

	// name labels the process.
	name string
}

// newHandle starts reaping cmd.
func newHandle(name string, cmd *osexec.Cmd) *handle {
	h := &handle{cmd: cmd, name: name, done: make(chan struct{})}
	go func() {
		h.err = cmd.Wait()
		close(h.done)
	}()
	return h
}

// Wait blocks until the process exits.
func (h *handle) Wait() error {
	<-h.done
	return h.err
}

// Stop sends SIGTERM to the process group, then SIGKILL when ctx ends first, and
// waits. A process that has already exited stops without error.
func (h *handle) Stop(ctx context.Context) error {
	select {
	case <-h.done:
		return nil
	default:
	}
	_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		_ = syscall.Kill(-h.cmd.Process.Pid, syscall.SIGKILL)
		<-h.done
		return apperr.Wrap(ctx.Err(), apperr.ContextCode(ctx.Err(), apperr.CodeTimeout),
			"stop "+h.name+": killed")
	}
}

// awaitReady polls ready with backoff until a verdict passes, the process exits, or
// ctx ends.
func (h *handle) awaitReady(
	ctx context.Context,
	ready interfaces.Probe[interfaces.ProcessHandle],
) error {
	wait := firstBackoff
	for {
		if v := ready.Run(ctx, h); v.Status == types.StatusPass {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-h.done:
			timer.Stop()
			return apperr.Wrap(
				h.err,
				apperr.CodeUpstream,
				h.name+" exited before it was ready",
			)
		case <-ctx.Done():
			timer.Stop()
			return apperr.Wrap(
				ctx.Err(),
				apperr.ContextCode(ctx.Err(), apperr.CodeTimeout),
				h.name+" was not ready",
			)
		case <-timer.C:
		}
		wait = min(2*wait, maxBackoff)
	}
}

// Package job provides the Job[T] type: a discoverer-backed, parallelised,
// optionally-retried unit of work implementing interfaces.AnyJob.
package job

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/engine"
)

// JobConfig holds execution parameters applied to every step in the job.
type JobConfig struct {
	// Concurrency is the max number of parallel workers; 0 means runtime.NumCPU.
	Concurrency int

	// Retry is the max number of retry attempts after a step failure (0 = no retry).
	Retry int

	// RetryDelay is the pause between retry attempts.
	RetryDelay time.Duration

	// WarnOnly downgrades Fail results to Warn so the job never blocks a gate.
	WarnOnly bool
}

// Option configures a JobConfig.
type Option func(*JobConfig)

// WithConcurrency sets the maximum number of parallel workers.
func WithConcurrency(n int) Option { return func(c *JobConfig) { c.Concurrency = n } }

// WithRetry sets the maximum number of retry attempts on step failure.
func WithRetry(n int) Option { return func(c *JobConfig) { c.Retry = n } }

// WithRetryDelay sets the pause between retry attempts.
func WithRetryDelay(
	d time.Duration,
) Option {
	return func(c *JobConfig) { c.RetryDelay = d }
}

// WithWarnOnly marks the job non-blocking: Fail results are downgraded to Warn.
func WithWarnOnly() Option { return func(c *JobConfig) { c.WarnOnly = true } }

// Job[T] discovers work items of type T, maps each to a WorkUnit, and
// fans them out in parallel. It implements interfaces.AnyJob.
// Field order minimises GC pointer-scan range: interfaces, func, strings, value types.
type Job[T any] struct {
	// discoverer yields the work items of type T at execution time.
	discoverer interfaces.Discoverer[T]

	// tool is the optional required tool; when set its binary is checked via
	// RequireTool and a missing binary turns the whole job into a skip.
	tool interfaces.ToolSpec

	// mapper converts each discovered item into the WorkUnit that is run.
	mapper func(root string, item T) engine.WorkUnit

	// name identifies the job (and single-unit steps) in StepResults.
	name string

	// group categorizes the job for display grouping.
	group string

	// config holds the concurrency, retry, and warn-only execution parameters.
	config JobConfig
}

// Compile-time assertion that *Job[string] satisfies interfaces.AnyJob.
var _ interfaces.AnyJob = (*Job[string])(nil)

// Builder[T] assembles a Job[T] via a fluent API.
// Field order minimises GC pointer-scan range: interfaces, func, strings, slices.
type Builder[T any] struct {
	// discoverer is the item source set via WithDiscoverer.
	discoverer interfaces.Discoverer[T]

	// tool is the optional required tool set via WithTool.
	tool interfaces.ToolSpec

	// mapper is the item-to-WorkUnit function set via WithMapper.
	mapper func(root string, item T) engine.WorkUnit

	// name is the job name carried into the built Job.
	name string

	// group is the display group carried into the built Job.
	group string

	// opts accumulate the execution options applied to JobConfig on Build.
	opts []Option
}

// New returns a Builder[T] for a job with the given name and group.
func New[T any](name, group string) *Builder[T] {
	return &Builder[T]{name: name, group: group}
}

// WithTool sets the ToolSpec checked by RequireTool before execution.
func (b *Builder[T]) WithTool(t interfaces.ToolSpec) *Builder[T] {
	b.tool = t
	return b
}

// WithDiscoverer sets the Discoverer that yields work items.
func (b *Builder[T]) WithDiscoverer(d interfaces.Discoverer[T]) *Builder[T] {
	b.discoverer = d
	return b
}

// WithMapper sets the function that converts (root, item) into a WorkUnit.
func (b *Builder[T]) WithMapper(
	fn func(root string, item T) engine.WorkUnit,
) *Builder[T] {
	b.mapper = fn
	return b
}

// WithOptions appends execution options.
func (b *Builder[T]) WithOptions(opts ...Option) *Builder[T] {
	b.opts = append(b.opts, opts...)
	return b
}

// Build constructs and returns the Job[T].
func (b *Builder[T]) Build() interfaces.AnyJob {
	cfg := JobConfig{}
	for _, o := range b.opts {
		o(&cfg)
	}
	return &Job[T]{
		discoverer: b.discoverer,
		mapper:     b.mapper,
		name:       b.name,
		group:      b.group,
		tool:       b.tool,
		config:     cfg,
	}
}

// Meta implements interfaces.AnyJob.
func (j *Job[T]) Meta() types.JobMeta {
	toolName := ""
	if j.tool != nil {
		toolName = j.tool.Binary()
	}
	return types.JobMeta{Name: j.name, Group: j.group, Tool: toolName}
}

// Execute implements interfaces.AnyJob.
// It checks the required tool (skip if missing), discovers items (skip if none),
// maps each to a WorkUnit, fans them out via FanOut, then optionally downgrades
// failures to warnings when WarnOnly is set.
func (j *Job[T]) Execute(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
) (types.StepResults, error) {
	if j.tool != nil && j.tool.Binary() != "" {
		if err := runner.RequireTool(j.tool.Binary(), j.tool.InstallHint()); err != nil {
			// Missing tool is a skip, not a fatal error - the job is optional.
			return types.StepResults{ //nolint:nilerr // a missing tool is a skip, not an error
				{Name: j.name, Status: types.StatusSkip, Error: err.Error()},
			}, nil
		}
	}

	items, err := j.discoverer.Discover(ctx, root)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return types.StepResults{{Name: j.name, Status: types.StatusSkip}}, nil
	}

	units := make([]engine.WorkUnit, len(items))
	for i, item := range items {
		units[i] = j.mapper(root, item)
	}

	results := engine.FanOut(
		ctx,
		units,
		j.config.Concurrency,
		func(ctx context.Context, wu engine.WorkUnit) types.StepResult {
			var runErr error
			if j.config.Retry > 0 {
				runErr = retryDo(ctx, func() error {
					return runner.Run(ctx, wu.Dir, wu.Exe, wu.Args...)
				}, j.config.Retry, j.config.RetryDelay)
			} else {
				runErr = runner.Run(ctx, wu.Dir, wu.Exe, wu.Args...)
			}
			r := types.StepResult{Name: wu.Name, Status: types.StatusPass}
			if runErr != nil {
				r.Status = types.StatusFail
				r.Error = runErr.Error()
			}
			return r
		},
	)

	if j.config.WarnOnly {
		for i := range results {
			if results[i].Status == types.StatusFail {
				results[i].Status = types.StatusWarn
			}
		}
	}

	return results, nil
}

// Single creates a Job[string] that runs exactly one command.
// tool.Cmd overrides the executable prefix when set (e.g. ["uv", "run", "ruff"]).
func Single(
	name, group string,
	tool interfaces.ToolSpec,
	argsFn func(root string) []string,
	dirFn func(root string) string,
	opts ...Option,
) interfaces.AnyJob {
	disc := interfaces.DiscovererFunc[string](
		func(_ context.Context, _ string) ([]string, error) {
			return []string{name}, nil
		},
	)

	mapFn := func(root, _ string) engine.WorkUnit {
		dir := dirFn(root)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		exe, args := tool.Cmd(argsFn(root)...)
		return engine.WorkUnit{Name: name, Dir: dir, Exe: exe, Args: args}
	}
	return New[string](name, group).
		WithTool(tool).
		WithDiscoverer(disc).
		WithMapper(mapFn).
		WithOptions(opts...).
		Build()
}

// retryDo calls fn up to maxRetries additional times after a failure.
func retryDo(
	ctx context.Context,
	fn func() error,
	maxRetries int,
	delay time.Duration,
) error {
	var last error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 && delay > 0 {
			select {
			case <-ctx.Done():
				return apperr.Wrap(
					ctx.Err(),
					apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
					"retry wait canceled",
				)
			case <-time.After(delay):
			}
		}
		last = fn()
		if last == nil {
			return nil
		}
	}
	return apperr.Wrap(
		last,
		apperr.CodeInternal,
		fmt.Sprintf("failed after %d attempt(s)", maxRetries+1),
	)
}

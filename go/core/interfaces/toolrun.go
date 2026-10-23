package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// StepCache remembers which steps passed on exactly these inputs, so a later run can
// skip them. A key is content-addressed (see foundation/stepcache.Key); only a pass is
// ever recorded.
type StepCache interface {
	// Hit reports whether key was recorded.
	Hit(ctx context.Context, key string) (bool, error)

	// Record marks key as passed. Recording a key twice is not an error.
	Record(ctx context.Context, key string) error
}

// ToolRunner executes tool runs: the presence check, target selection, fan-out, the
// step cache and the output-to-verdict mapping.
type ToolRunner interface {
	// Run executes run over cb under opts (fix arguments, strict presence, placeholder
	// values) and returns one step per invocation (or one skipped or failed step when the
	// tool is missing). The error is set only when the run could not start: an unknown
	// tool, selector or parser, a bad cwd, a placeholder with no value, or a selector
	// that failed.
	Run(
		ctx context.Context,
		cb Codebase,
		run types.ToolRun,
		opts types.RunOptions,
	) (types.StepResults, error)
}

// TargetSelector lists a tool run's targets in a codebase.
type TargetSelector interface {
	// Select returns the targets spec selects in cb, sorted: files or directories,
	// relative to the codebase root.
	Select(ctx context.Context, cb Codebase, spec types.TargetSpec) ([]string, error)
}

// Parser turns a tool's captured output into a verdict.
type Parser interface {
	// Parse reads res and returns the verdict, with any findings the output reports.
	Parse(res CmdResult) types.Verdict
}

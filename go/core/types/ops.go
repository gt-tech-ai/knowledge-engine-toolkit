package types

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// SafetyClass ranks what an operation may do to the developer's machine and the
// environments it can reach. Exposure policies admit an operation when its class
// ranks at or below the policy's ceiling, so the order is load-bearing.
type SafetyClass string

const (
	// SafetyReadOnly reads the tree and reports; it changes no file and no remote state.
	SafetyReadOnly SafetyClass = "read_only"
	// SafetyMutating rewrites files in the working tree (format, codegen, tidy) or starts
	// local processes, but touches no shared or remote state.
	SafetyMutating SafetyClass = "mutating"
	// SafetyDestructive deletes local state that cannot be regenerated from the tree
	// (volumes, databases, clusters).
	SafetyDestructive SafetyClass = "destructive"
	// SafetyCredentialed acts under cloud credentials against a shared environment.
	SafetyCredentialed SafetyClass = "credentialed"
)

// safetyRanks returns c's position in the total order read_only < mutating < destructive
// < credentialed; ok is false for an unknown class.
func safetyRanks(c SafetyClass) (int, bool) {
	switch c {
	case SafetyReadOnly:
		return 0, true
	case SafetyMutating:
		return 1, true
	case SafetyDestructive:
		return 2, true
	case SafetyCredentialed:
		return 3, true
	default:
		return 0, false
	}
}

// Rank returns the class's position in the total order; ok is false for an unknown class.
func (c SafetyClass) Rank() (int, bool) {
	return safetyRanks(c)
}

// OpLifecycle says how a front-end runs an operation: inline, as a background run, or
// only with a human at the terminal.
type OpLifecycle string

const (
	// OpLifecycleBounded finishes within its descriptor timeout; a front-end runs it
	// inline.
	OpLifecycleBounded OpLifecycle = "bounded"
	// OpLifecycleLongRunning may outlast a request; a front-end starts it as a background
	// run.
	OpLifecycleLongRunning OpLifecycle = "long_running"
	// OpLifecycleInteractive needs a human to answer prompts; only a front-end with a
	// human at the terminal runs it.
	OpLifecycleInteractive OpLifecycle = "interactive"
)

// Valid reports whether l is one of the documented lifecycles.
func (l OpLifecycle) Valid() bool {
	switch l {
	case OpLifecycleBounded, OpLifecycleLongRunning, OpLifecycleInteractive:
		return true
	default:
		return false
	}
}

// OperationDescriptor is the data describing one developer operation: what a
// front-end lists, how it filters, and how long a run may take.
type OperationDescriptor struct {
	// Name is the operation's stable id (e.g. "test.unit"); front-ends use it as the
	// tool / command name.
	Name string `yaml:"name"`
	// Group is the catalog section the operation is listed under.
	Group string `yaml:"group"`
	// Summary is the one-line description a front-end shows.
	Summary string `yaml:"summary"`
	// SafetyClass is what the operation may change; exposure policies filter on it.
	SafetyClass SafetyClass `yaml:"safety_class"`
	// Lifecycle says whether the operation runs inline, in the background, or only
	// interactively.
	Lifecycle OpLifecycle `yaml:"lifecycle"`
	// Timeout bounds one run; the registry's timeout decorator enforces it.
	Timeout time.Duration `yaml:"timeout"`
}

// ReservedOperationPrefix is the name prefix of a front-end's own background-run tools
// (runs.status, runs.result, …); no operation may use it, so the two never collide.
const ReservedOperationPrefix = "runs."

// Validate rejects a descriptor with an empty name, a name carrying
// ReservedOperationPrefix, an unknown safety class or lifecycle, or a non-positive
// timeout, as a CodeInvalidInput error naming the operation.
func (d OperationDescriptor) Validate() error {
	if d.Name == "" {
		return apperr.New(
			apperr.CodeInvalidInput,
			"operation descriptor has an empty name",
		)
	}
	if _, ok := d.SafetyClass.Rank(); !ok {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation %q: unknown safety class %q (want read_only, mutating, destructive or "+
				"credentialed)",
			d.Name,
			d.SafetyClass,
		))
	}
	if strings.HasPrefix(d.Name, ReservedOperationPrefix) {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation %q: the %q name prefix is reserved for the background-run tools",
			d.Name,
			ReservedOperationPrefix,
		))
	}
	if !d.Lifecycle.Valid() {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation %q: unknown lifecycle %q (want bounded, long_running or interactive)",
			d.Name,
			d.Lifecycle,
		))
	}
	if d.Timeout <= 0 {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation %q: timeout must be positive, got %s", d.Name, d.Timeout,
		))
	}
	return nil
}

// ExposurePolicy is the data that decides which operations one front-end may
// list and run, and how its operations confirm: an operation is admitted when its class
// ranks at or below MaxSafety, its lifecycle is in Lifecycles, it is in Allow (when Allow
// is non-empty), and it is not in Deny.
type ExposurePolicy struct {
	// MaxSafety is the highest safety class the policy admits.
	MaxSafety SafetyClass `yaml:"max_safety"`
	// Prompter names the Prompter kind the front-end resolves for the admitted
	// operations (for example one that asks at a terminal, or one that refuses); the
	// registry does not interpret it.
	Prompter string `yaml:"prompter"`
	// Lifecycles lists the lifecycles the policy admits.
	Lifecycles []OpLifecycle `yaml:"lifecycles"`
	// Allow, when non-empty, narrows the policy to these operation names.
	Allow []string `yaml:"allow"`
	// Deny removes these operation names from the policy.
	Deny []string `yaml:"deny"`
}

// Validate rejects an unknown MaxSafety or lifecycle as a CodeInvalidInput error naming
// the policy. Operation names and the prompter kind are checked where they resolve —
// against the registered operations and by the front-end.
func (p ExposurePolicy) Validate(name string) error {
	if _, ok := p.MaxSafety.Rank(); !ok {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"exposure policy %q: unknown max_safety %q", name, p.MaxSafety,
		))
	}
	for _, l := range p.Lifecycles {
		if !l.Valid() {
			return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"exposure policy %q: unknown lifecycle %q", name, l,
			))
		}
	}
	return nil
}

// MCPSettings is the data that bounds what the MCP front-end keeps and returns:
// how much of a failed step's output a tool result carries, and how many run log
// directories survive a server start.
type MCPSettings struct {
	// RunDir is the directory, relative to the working-tree root, holding one directory
	// of step logs per tool call.
	RunDir string `yaml:"run_dir"`
	// TailLines is how many trailing output lines a failed step contributes to a tool
	// result; a passing step contributes none.
	TailLines int `yaml:"tail_lines"`
	// RunRetention is how many of the newest run directories a server start keeps, and
	// how many finished background runs the server remembers.
	RunRetention int `yaml:"run_retention"`
}

// Validate rejects a non-positive TailLines or RunRetention, or a RunDir that is empty,
// absolute, or escapes the working-tree root, as a CodeInvalidInput error naming the
// field.
func (s MCPSettings) Validate() error {
	switch {
	case s.TailLines <= 0:
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"ops.mcp: tail_lines must be > 0, got %d", s.TailLines,
		))
	case s.RunRetention <= 0:
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"ops.mcp: run_retention must be > 0, got %d", s.RunRetention,
		))
	case s.RunDir == "":
		return apperr.New(apperr.CodeInvalidInput, "ops.mcp: run_dir is required")
	case !filepath.IsLocal(s.RunDir):
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"ops.mcp: run_dir must be a relative path under the working-tree root, got %q",
			s.RunDir,
		))
	}
	return nil
}

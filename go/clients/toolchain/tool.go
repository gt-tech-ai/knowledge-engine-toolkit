package toolchain

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time check: Tool implements interfaces.ToolSpec.
var _ interfaces.ToolSpec = (*Tool)(nil)

// Tool is the lightweight representation of an external binary (e.g. buf, ruff, go).
type Tool struct {
	// name is the human-readable display name.
	name string

	// binary is the executable name looked up on PATH.
	binary string

	// installHint is shown when the binary is missing from PATH.
	installHint string

	// prefix is an optional command prefix (e.g. ["uv", "run"]).
	prefix []string
}

// NewTool creates a Tool with the given name, binary, install hint, and optional
// command prefix (e.g. ["uv", "run"] for Python tools invoked via uv).
func NewTool(name, binary, installHint string, prefix []string) *Tool {
	return &Tool{
		name:        name,
		binary:      binary,
		installHint: installHint,
		prefix:      prefix,
	}
}

// Name returns the human-readable display name.
func (t *Tool) Name() string { return t.name }

// Binary returns the executable name on PATH.
func (t *Tool) Binary() string { return t.binary }

// InstallHint returns installation instructions shown when the tool is missing.
func (t *Tool) InstallHint() string { return t.installHint }

// Cmd builds the full command slice with prefix expansion.
// For tools with a prefix (e.g. "uv run ruff"), Cmd("check", ".") returns
// ("uv", ["run", "ruff", "check", "."]).
// For tools without a prefix, returns (binary, args).
func (t *Tool) Cmd(args ...string) (exe string, cmdArgs []string) {
	if len(t.prefix) == 0 {
		return t.binary, args
	}
	full := make([]string, 0, len(t.prefix)-1+1+len(args))
	full = append(full, t.prefix[1:]...)
	full = append(full, t.binary)
	full = append(full, args...)
	return t.prefix[0], full
}

// RequireTool checks that the tool is available on PATH via the provided runner.
func RequireTool(runner interfaces.CommandRunner, t interfaces.ToolSpec) error {
	return runner.RequireTool(t.Binary(), t.InstallHint())
}

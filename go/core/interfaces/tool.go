package interfaces

// ToolSpec describes an external CLI tool requirement.
// It captures the tool's identity, availability check, and command construction.
type ToolSpec interface {
	// Name returns the display name (e.g., "golangci-lint").
	Name() string

	// Binary returns the executable name on PATH (e.g., "golangci-lint").
	Binary() string

	// InstallHint returns installation instructions.
	InstallHint() string

	// Cmd builds the full command: (executable, args) with prefix handling.
	// For tools with a prefix (e.g., "uv run ruff"), Cmd("check", ".")
	// returns ("uv", ["run", "ruff", "check", "."]).
	Cmd(args ...string) (exe string, cmdArgs []string)
}

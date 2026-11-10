package types

// ToolCheckResult is returned when checking a tool.
type ToolCheckResult struct {
	// Tool is the spec that was checked.
	Tool *ToolDef

	// Found is the detected version (or "installed"/"unknown"), for reporting.
	Found string

	// Message is the human-readable status line.
	Message string

	// Status classifies the outcome (OK / Outdated / Missing).
	Status ToolStatus
}

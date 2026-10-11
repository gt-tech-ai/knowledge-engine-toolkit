package types

// JobMeta describes a CLI execution job's identity for display, grouping,
// and tool resolution within the execution engine.
type JobMeta struct {
	// Name is the human-readable job name shown in result tables.
	Name string

	// Group is the phase or category label used to group jobs in result output.
	Group string

	// Tool is the binary name resolved via the tool registry.
	// Empty when the job has no external tool dependency.
	Tool string

	// WarnOnly downgrades failures to warnings so the gate continues past
	// this job even when it produces errors.
	WarnOnly bool
}

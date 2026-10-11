package engine

// WorkUnit is a declarative command descriptor produced by a job mapper.
// It is the unit of work fanned out by Job[T].Execute.
type WorkUnit struct {
	// Name is the step name reported in StepResult (typically the module or target path).
	Name string

	// Dir is the working directory for the command.
	Dir string

	// Exe is the executable to run.
	Exe string

	// Args are the command-line arguments passed to Exe.
	Args []string
}

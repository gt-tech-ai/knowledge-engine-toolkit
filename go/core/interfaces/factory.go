package interfaces

// JobFactory is a typed producer of an AnyJob. CLI pipeline types implement it
// so a job is a modeled value (with its parameters as fields) rather than a
// bare factory function. Callers invoke Job() to obtain the runnable AnyJob.
type JobFactory interface {
	// Job builds and returns the configured AnyJob.
	Job() AnyJob
}

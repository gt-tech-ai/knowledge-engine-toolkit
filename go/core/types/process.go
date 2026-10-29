package types

import "io"

// ProcessSpec describes a process to start.
type ProcessSpec struct {
	// Stdout receives the process's standard output; nil discards it.
	Stdout io.Writer

	// Stderr receives the process's standard error; nil discards it.
	Stderr io.Writer

	// Name labels the process in errors and logs.
	Name string

	// Dir is the working directory; empty is the caller's.
	Dir string

	// LogFile, when set, is a file the process's stdout and stderr (those not given a
	// writer) are appended to, created 0600: a detached process outlives its caller,
	// so its output can only go to a file.
	LogFile string

	// Args are the program and its arguments.
	Args []string

	// Env are extra `KEY=value` entries added to the caller's environment.
	Env []string
}

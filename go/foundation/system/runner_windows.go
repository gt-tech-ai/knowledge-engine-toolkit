//go:build windows

package system

import "os/exec"

// configureProcessGroup is a no-op on Windows: there are no POSIX process groups
// or SIGINT semantics, so the runner keeps exec.CommandContext's default cancel
// behaviour (terminate the direct child). Full process-tree teardown on Windows
// would require Job Objects or `taskkill /T`; it is intentionally out of scope —
// this monorepo's dev and CI run on macOS/Linux (the unix path above). Defined so
// the runner compiles and runs on Windows; add a Job-Object implementation here
// if Windows becomes a target.
func configureProcessGroup(_ *exec.Cmd) {}

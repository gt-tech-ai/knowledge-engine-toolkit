//go:build unix

package exec

import (
	osexec "os/exec"
	"syscall"
)

// ownGroup starts cmd in its own process group, so Stop reaches the children it starts.
func ownGroup(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// ownSession starts cmd in a new session, so it outlives the caller's terminal.
func ownSession(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// terminate sends SIGTERM to cmd's process group; a group already gone is ignored.
func terminate(cmd *osexec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// kill sends SIGKILL to cmd's process group; a group already gone is ignored.
func kill(cmd *osexec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

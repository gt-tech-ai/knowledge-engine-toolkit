//go:build unix

package system

import (
	"os/exec"
	"syscall"
	"time"
)

// processGroupGrace is how long a cancelled child's process group has to exit
// after the graceful SIGTERM before os/exec escalates to SIGKILL on the child.
const processGroupGrace = 5 * time.Second

// configureProcessGroup puts the child in its own process group and wires
// graceful, group-scoped teardown on context cancellation: the whole group is
// sent SIGTERM (so grandchildren — e.g. the test binaries a `go test` spawns —
// are torn down too, not orphaned), and after a grace window os/exec escalates
// to SIGKILL on the child. exec.CommandContext's default cancel SIGKILLs only
// the direct child, leaving grandchildren running; this fixes that.
//
// SIGTERM, not SIGINT: POSIX shells set backgrounded jobs to *ignore* SIGINT (so
// a foreground Ctrl+C won't kill background work), so a group SIGINT leaves such
// grandchildren alive; SIGTERM is the conventional, non-ignored terminate signal
// for programmatic teardown.
//
// It is applied to all runner-launched commands, which are batch or streaming
// (go test, lint, vitest, docker compose, `tilt up --stream`) — none read from
// the controlling TTY for interactive input, so moving them to their own process
// group does not break job control. Streaming children like `tilt up --stream`
// are launched via the runner too, so they get the same group teardown on
// cancel (the CLI's root signal handler cancels the context → SIGTERM to the
// group). Detached docker-daemon containers (`compose up -d`) live outside this
// process tree and are torn down separately by an explicit `compose down`.
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID targets the whole process group (the child is the group
		// leader because Setpgid made its PID == PGID).
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
			// Fall back to signalling just the child if the group send fails
			// (e.g. the group already gone).
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil
	}
	cmd.WaitDelay = processGroupGrace
}

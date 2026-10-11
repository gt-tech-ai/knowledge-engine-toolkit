//go:build unix

package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/system"
)

// TestRunner_CancelTearsDownProcessGroup tests that cancelling the context tears
// down the child's entire process group, leaving no orphaned grandchild.
//
// Why this test is important:
//   - exec.CommandContext's default cancel SIGKILLs only the direct child, so a
//     grandchild (e.g. a dev server spawned by a shell wrapper) would survive
//     cancellation and leak — the runner's Setpgid + group-SIGTERM teardown is
//     what prevents stray long-lived processes after Ctrl+C.
//
// What it tests:
//   - After cancelling a `sh -c` that backgrounds a 120s sleep, the recorded
//     grandchild PID is reaped (signal 0 returns ESRCH), proving the whole group
//     was torn down rather than just the direct child.
func TestRunner_CancelTearsDownProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	// Child (sh) spawns a long sleep (grandchild), records its PID, then waits.
	script := "sleep 120 & echo $! > " + pidFile + "; wait"

	ctx, cancel := context.WithCancel(context.Background())
	r := system.NewRunner()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, dir, "sh", "-c", script) }()

	var gcPid int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		gcPid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return gcPid > 0
	}, 5*time.Second, 20*time.Millisecond, "grandchild should start and record its PID")

	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		require.Fail(t, "runner did not return after cancel")
	}

	// The grandchild must be gone (signal 0 returns ESRCH for a dead process).
	require.Eventually(t, func() bool {
		return syscall.Kill(gcPid, 0) != nil
	}, 10*time.Second, 50*time.Millisecond,
		"grandchild must be torn down with the group, not orphaned")

	_ = syscall.Kill(gcPid, syscall.SIGKILL) // backstop: never leak a 120s sleep
}

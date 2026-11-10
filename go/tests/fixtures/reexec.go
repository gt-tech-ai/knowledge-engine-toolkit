package fixtures

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// reexecEnv marks a test binary that ReexecTest started to run one test alone.
const reexecEnv = "KE_FIXTURES_REEXEC"

// InReexec reports whether this process is the child ReexecTest started, where the test
// owns the whole process and may change process-global state.
func InReexec() bool { return os.Getenv(reexecEnv) == "1" }

// ReexecTest runs the test named name again, alone, in a child process of the current
// test binary, and fails tb with the child's output if it fails. A test that swaps the
// process stdio or counts every goroutine in the process cannot run beside parallel
// tests; the parent calls this and returns, and the child (where InReexec is true) runs
// the real body.
func ReexecTest(tb testing.TB, name string) {
	tb.Helper()

	exe, err := os.Executable()
	require.NoError(tb, err)

	cmd := exec.CommandContext(tb.Context(), exe, "-test.run=^"+name+"$", "-test.count=1")

	cmd.Env = append(os.Environ(), reexecEnv+"=1")

	out, err := cmd.CombinedOutput()
	require.NoError(tb, err, "the re-executed test %s failed:\n%s", name, out)
}

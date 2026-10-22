package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// goWorkspace is the fixed absolute root the path assertions in this file join onto.
const goWorkspace = "/ws/go"

// TestClientsToolchainAddToPathIfMissing_Idempotent verifies PATH mutation is idempotent.
//
// Why this test is important:
//   - The PATH helpers run on every setup install; a non-idempotent add would
//     grow PATH without bound across repeated setup runs.
//
// What it tests:
//   - Adding the same directory twice leaves exactly one entry on PATH.
func TestClientsToolchainAddToPathIfMissing_Idempotent(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	toolchain.AddToPathIfMissing("/opt/tool/bin", fixtures.NopLogger())
	toolchain.AddToPathIfMissing("/opt/tool/bin", fixtures.NopLogger())

	count := 0
	for _, e := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.EqualFold(e, "/opt/tool/bin") {
			count++
		}
	}
	assert.Equal(t, 1, count, "dir should appear exactly once on PATH")
}

// TestClientsToolchainProvision_ResolveGoBin_EnvDriven verifies the go-install bin
// directory resolution through the public Provision flow: Provision runs
// ensureGoBinInPath, which resolves the go-install bin dir and prepends it to PATH, so
// the resolved directory is observable as a PATH entry after Provision.
//
// Why this test is important:
//   - The resolved directory gates the PATH entry added after `go install`; a
//     wrong directory means freshly-installed Go tools are never found and setup
//     falsely reports them missing.
//
// What it tests:
//   - GOBIN is used directly; else GOPATH/bin; else the ~/go/bin fallback.
func TestClientsToolchainProvision_ResolveGoBin_EnvDriven(t *testing.T) {
	provision := func(t *testing.T) {
		t.Helper()
		ctrl := gomock.NewController(t)
		r := mocks.NewMockCommandRunner(ctrl)
		registerToolScanMock(r) // hermetic tool scan (Exists/RunBuffered)
		inst := toolchain.NewInstaller(
			r,
			fixtures.NopLogger(),
			"linux-apt",
			denyPrompter(t),
		)
		// checkOnly=true installs nothing; ensureGoBinInPath still runs and mutates PATH.
		_ = inst.Provision(
			context.Background(),
			"/root",
			provisionCatalog(t),
			false,
			true,
		)
	}

	onPath := func(want string) bool {
		return slices.Contains(filepath.SplitList(os.Getenv("PATH")), want)
	}

	t.Run("GOBIN is used directly", func(t *testing.T) {
		t.Setenv("PATH", "/usr/bin")
		t.Setenv("GOBIN", "/custom/gobin")
		provision(t)
		assert.True(
			t,
			onPath("/custom/gobin"),
			"GOBIN must be added to PATH, got %q",
			os.Getenv("PATH"),
		)
	})

	t.Run("GOPATH/bin when GOBIN unset", func(t *testing.T) {
		t.Setenv("PATH", "/usr/bin")
		t.Setenv("GOBIN", "")
		t.Setenv("GOPATH", "/ws/go")
		provision(t)
		assert.True(t, onPath(filepath.Join(goWorkspace, "bin")),
			"GOPATH/bin must be added to PATH, got %q", os.Getenv("PATH"))
	})

	t.Run("~/go/bin fallback when GOBIN and GOPATH unset", func(t *testing.T) {
		t.Setenv("PATH", "/usr/bin")
		t.Setenv("GOBIN", "")
		t.Setenv("GOPATH", "")
		provision(t)
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("home dir unresolvable on this host")
		}
		assert.True(t, onPath(filepath.Join(home, "go", "bin")),
			"~/go/bin must be added to PATH, got %q", os.Getenv("PATH"))
	})
}

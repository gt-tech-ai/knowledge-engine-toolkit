package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
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
	t.Setenv(textPath, textUsrBin)
	toolchain.AddToPathIfMissing(textOptToolBin, fixtures.NopLogger())
	toolchain.AddToPathIfMissing(textOptToolBin, fixtures.NopLogger())

	count := 0

	for _, e := range filepath.SplitList(os.Getenv(textPath)) {
		if strings.EqualFold(e, textOptToolBin) {
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
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	cases := []struct {
		name, gobin, gopath, want string
	}{
		{"GOBIN is used directly", "/custom/gobin", "", "/custom/gobin"},
		{
			"GOPATH/bin when GOBIN unset",
			"",
			"/ws/go",
			filepath.Join(goWorkspace, textBin),
		},
		{
			"~/go/bin fallback when GOBIN and GOPATH unset",
			"",
			"",
			filepath.Join(home, textGoLower, textBin),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(textPath, textUsrBin)
			t.Setenv(textGobin, testCase.gobin)
			t.Setenv("GOPATH", testCase.gopath)
			provisionCheckOnly(t)
			assert.Contains(t, filepath.SplitList(os.Getenv(textPath)), testCase.want)
		})
	}
}

// provisionCheckOnly runs a check-only Provision, which installs nothing but still
// adds the resolved Go bin directory to PATH.
func provisionCheckOnly(t *testing.T) {
	t.Helper()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	registerToolScanMock(r) // hermetic tool scan (Exists/RunBuffered)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), textLinuxApt, denyPrompter(t))

	_ = inst.Provision(
		context.Background(),
		textRoot,
		provisionCatalog(t),
		fixtures.BuildRef(func(row *types.InstallToolsOptions) {
			row.CheckOnly = true
		}),
	)
}

const (
	// textOptToolBin is the literal "/opt/tool/bin".
	textOptToolBin = "/opt/tool/bin"
)

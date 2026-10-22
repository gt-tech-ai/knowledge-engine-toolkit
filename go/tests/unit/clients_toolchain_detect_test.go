package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// goVersionParse is a minimal "go version go1.X.Y ..." parser for the detection
// fixtures, standing in for the manifest's go-version strategy.
func goVersionParse(s string) string {
	const prefix = "go version go"
	if len(s) > len(prefix) {
		rest := s[len(prefix):]
		for i := range len(rest) {
			if rest[i] == ' ' {
				return rest[:i]
			}
		}
		return rest
	}
	return ""
}

// TestClientsToolchainDetector_Scan_Classification pins the detection contract.
//
// Why this test is important:
//   - Detection drives whether setup installs, skips, or warns; a wrong status
//     means a present tool is reinstalled, or a stale/broken tool is treated as OK.
//
// What it tests (one tool per status path, against a mock runner):
//   - missing binary -> Missing; versionless present tool -> OK.
//   - up-to-date version -> OK; below-minimum version -> Outdated.
//   - version-command failure -> Outdated; unparseable version -> OK (best-effort).
func TestClientsToolchainDetector_Scan_Classification(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)

	specs := []types.ToolDef{
		{Name: "missing", Binary: "missingbin"},
		{Name: "versionless", Binary: "lefthook"},
		{
			Name: "current", Binary: "go",
			MinVersion:   &types.Version{Major: 1, Minor: 26},
			VersionCmd:   []string{"go", "version"},
			VersionParse: goVersionParse,
		},
		{
			Name: "stale", Binary: "node",
			MinVersion:   &types.Version{Major: 20, Minor: 0},
			VersionCmd:   []string{"node", "--version"},
			VersionParse: types.ParseFirstVersion,
		},
		{
			Name: "broken-version-cmd", Binary: "buf",
			MinVersion: &types.Version{Major: 1, Minor: 0},
			VersionCmd: []string{"buf", "--version"},
		},
		{
			Name: "unparseable-version", Binary: "atlas",
			MinVersion: &types.Version{Major: 1, Minor: 0},
			VersionCmd: []string{"atlas", "version"},
		},
	}

	r.EXPECT().Exists("missingbin").Return(false)
	r.EXPECT().Exists("lefthook").Return(true)
	r.EXPECT().Exists("go").Return(true)
	r.EXPECT().Exists("node").Return(true)
	r.EXPECT().Exists("buf").Return(true)
	r.EXPECT().Exists("atlas").Return(true)
	r.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), "go", "version").
		Return(interfaces.CmdResult{Stdout: []byte("go version go1.26.0 darwin/arm64")})
	r.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), "node", "--version").
		Return(interfaces.CmdResult{Stdout: []byte("v18.19.0")})
	r.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), "buf", "--version").
		Return(interfaces.CmdResult{Err: assert.AnError})
	r.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), "atlas", "version").
		Return(interfaces.CmdResult{Stdout: []byte("no version digits here")})

	det := toolchain.NewDetector(r, fixtures.NopLogger())
	results := det.Scan(context.Background(), "/root", specs)

	require.Len(t, results, 6)
	assert.Equal(t, types.StatusToolMissing, results[0].Status)
	assert.Equal(t, types.StatusToolOK, results[1].Status)
	assert.Equal(t, types.StatusToolOK, results[2].Status)
	assert.Equal(t, types.StatusToolOutdated, results[3].Status)
	assert.Equal(
		t, types.StatusToolOutdated, results[4].Status,
		"version-cmd failure -> outdated",
	)
	assert.Equal(
		t, types.StatusToolOK, results[5].Status,
		"unparseable version -> OK (best-effort)",
	)
}

// TestClientsToolchainDetector_CheckPrerequisites_Missing verifies prerequisite
// reporting.
//
// Why this test is important:
//   - Prerequisites (Go, Docker) are never auto-installed; a missing one must
//     abort setup with an actionable, named error rather than a later cryptic
//     build failure.
//
// What it tests:
//   - A missing prerequisite yields a CodeUnavailable error naming the tool.
//   - SummarizeMissing folds the prerequisite error into the dependency summary.
func TestClientsToolchainDetector_CheckPrerequisites_Missing(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Exists("docker").Return(false)

	det := toolchain.NewDetector(r, fixtures.NopLogger())
	prereqs := []types.ToolDef{
		{
			Name:        "Docker",
			Binary:      "docker",
			FallbackURL: "https://docs.docker.com/get-docker/",
		},
	}

	err := det.CheckPrerequisites(context.Background(), "/root", prereqs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Docker")

	summary := toolchain.SummarizeMissing(err, nil)
	require.Error(t, summary)
	assert.Contains(t, summary.Error(), "Docker")
}

// TestClientsToolchainSummarizeMissing_OutdatedAndPrereq covers the dependency-summary
// formatting.
//
// Why this test is important:
//   - SummarizeMissing produces the single "here is everything missing" message
//     for `setup --check`; dropping the version detail or fallback URL leaves the
//     developer without the information needed to fix the environment.
//
// What it tests:
//   - All-OK results (no prereq error) summarize to nil.
//   - An outdated tool's name, found/required versions, and fallback URL all
//     appear in the consolidated error.
func TestClientsToolchainSummarizeMissing_OutdatedAndPrereq(t *testing.T) {
	t.Parallel()

	require.NoError(t, toolchain.SummarizeMissing(nil, []types.ToolCheckResult{
		{Tool: &types.ToolDef{Name: "go"}, Status: types.StatusToolOK},
	}))

	outdated := []types.ToolCheckResult{{
		Tool: &types.ToolDef{
			Name:        "golangci-lint",
			MinVersion:  &types.Version{Major: 1, Minor: 60},
			FallbackURL: "https://golangci-lint.run",
		},
		Status: types.StatusToolOutdated,
		Found:  "1.55.0",
	}}
	err := toolchain.SummarizeMissing(assert.AnError, outdated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "golangci-lint")
	assert.Contains(t, err.Error(), "1.55.0")
	assert.Contains(t, err.Error(), "1.60")
	assert.Contains(t, err.Error(), "https://golangci-lint.run")
}

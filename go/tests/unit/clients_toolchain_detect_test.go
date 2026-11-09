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
	commandRunner := mocks.NewMockCommandRunner(ctrl)

	specs := testClientsToolchainDetectorScanClassificationTable()

	commandRunner.EXPECT().Exists("missingbin").Return(false)
	commandRunner.EXPECT().Exists("lefthook").Return(true)
	commandRunner.EXPECT().Exists(textGoLower).Return(true)
	commandRunner.EXPECT().Exists("node").Return(true)
	commandRunner.EXPECT().Exists(textBuf).Return(true)
	commandRunner.EXPECT().Exists("atlas").Return(true)
	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), textGoLower, textVersion).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("go version go1.26.0 darwin/arm64")
		}))
	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), "node", textVersionFlag).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("v18.19.0")
		}))
	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), textBuf, textVersionFlag).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Err = assert.AnError
		}))
	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), "atlas", textVersion).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("no version digits here")
		}))

	det := toolchain.NewDetector(commandRunner, fixtures.NopLogger())
	results := det.Scan(context.Background(), textRoot, specs)

	require.Len(t, results, wantResultsLen)
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

// testClientsToolchainDetectorScanClassificationTable builds the table
// TestClientsToolchainDetector_Scan_Classification runs.
func testClientsToolchainDetectorScanClassificationTable() []types.ToolDef {
	return []types.ToolDef{
		fixtures.Build(func(row *types.ToolDef) {
			row.Name = textMissing
			row.Binary = "missingbin"
		}),
		fixtures.Build(func(row *types.ToolDef) {
			row.Name = "versionless"
			row.Binary = "lefthook"
		}),
		fixtures.Build(func(row *types.ToolDef) {
			row.Name = "current"
			row.Binary = textGoLower
			row.MinVersion = &types.Version{Major: 1, Minor: minor}
			row.VersionCmd = []string{textGoLower, textVersion}
			row.VersionParse = goVersionParse
		}),
		fixtures.Build(func(row *types.ToolDef) {
			row.Name = textStale
			row.Binary = "node"
			row.MinVersion = &types.Version{Major: major, Minor: 0}
			row.VersionCmd = []string{"node", textVersionFlag}
			row.VersionParse = types.ParseFirstVersion
		}),
		fixtures.Build(func(row *types.ToolDef) {
			row.Name = "broken-version-cmd"
			row.Binary = textBuf
			row.MinVersion = &types.Version{Major: 1, Minor: 0}
			row.VersionCmd = []string{textBuf, textVersionFlag}
		}),
		fixtures.Build(func(row *types.ToolDef) {
			row.Name = "unparseable-version"
			row.Binary = "atlas"
			row.MinVersion = &types.Version{Major: 1, Minor: 0}
			row.VersionCmd = []string{"atlas", textVersion}
		}),
	}
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
	r.EXPECT().Exists(textDocker).Return(false)

	det := toolchain.NewDetector(r, fixtures.NopLogger())
	prereqs := []types.ToolDef{
		fixtures.Build(func(row *types.ToolDef) {
			row.Name = textDockerTitle
			row.Binary = textDocker
			row.FallbackURL = "https://docs.docker.com/get-docker/"
		}),
	}

	err := det.CheckPrerequisites(context.Background(), textRoot, prereqs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), textDockerTitle)

	summary := toolchain.SummarizeMissing(err, nil)
	require.Error(t, summary)
	assert.Contains(t, summary.Error(), textDockerTitle)
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
		{
			Tool: fixtures.BuildRef(func(row *types.ToolDef) {
				row.Name = textGoLower
			}),
			Status:  types.StatusToolOK,
			Found:   "",
			Message: "",
		},
	}))

	outdated := []types.ToolCheckResult{{
		Tool: fixtures.BuildRef(func(row *types.ToolDef) {
			row.Name = textGolangciLint
			row.MinVersion = &types.Version{Major: 1, Minor: minorPrereq}
			row.FallbackURL = "https://golangci-lint.run"
		}),
		Status:  types.StatusToolOutdated,
		Found:   "1.55.0",
		Message: "",
	}}
	err := toolchain.SummarizeMissing(assert.AnError, outdated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), textGolangciLint)
	assert.Contains(t, err.Error(), "1.55.0")
	assert.Contains(t, err.Error(), "1.60")
	assert.Contains(t, err.Error(), "https://golangci-lint.run")
}

const (
	// major is the major the tests use.
	major = 20

	// minor is the minor the tests use.
	minor = 26

	// minorPrereq is the minor prereq the tests use.
	minorPrereq = 60

	// wantResultsLen is the expected results len.
	wantResultsLen = 6
)

const (
	// textDockerTitle is the literal "Docker".
	textDockerTitle = "Docker"
)

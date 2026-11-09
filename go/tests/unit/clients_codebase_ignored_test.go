package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientcodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestCodebaseClient_GitKindIncludeIgnored tests the IncludeIgnored selector on the git
// kind.
//
// Why this test is important:
//   - A guard over a gitignored generated tier (a generated validator written into its
//     app package) must see those files, or it passes while judging nothing; every other
//     selector must still never see a build artifact, and listing every ignored file of
//     a repo (node_modules, virtualenvs) on each run would be ruinous.
//
// What it tests:
//   - A selector without IncludeIgnored never lists the ignored file, and runs no ignored
//     listing.
//   - With IncludeIgnored, git lists the ignored files only under the selector's
//     narrowest scope — its Paths, else its Roots, else its globs' literal directory
//     prefixes; the ignored file is marked Ignored and Untracked and still passes the
//     selector's own filters (the glob drops the ignored .txt).
//   - A glob with no literal prefix lists the whole tree's ignored files.
func TestCodebaseClient_GitKindIncludeIgnored(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		textAGo: textPackageA, textOutZzGenGo: "package out\n", "out/log.txt": "x\n",
	})

	runner := mocks.NewMockCommandRunner(gomock.NewController(t))
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--cached").
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("a.go\x00")
		})).
		AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--others",
		"--exclude-standard").Return(stdx.Zero[interfaces.CmdResult]()).AnyTimes()

	ignored := []byte("out/log.txt\x00out/zz_gen.go\x00")

	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--others",
		"--ignored", "--exclude-standard", "--", textOut).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = ignored
		})).
		Times(1)
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--others",
		"--ignored", "--exclude-standard", "--", textOutZzGenGo).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte("out/zz_gen.go\x00")
		})).
		Times(1)
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--others",
		"--ignored", "--exclude-standard").
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = ignored
		})).
		Times(1)

	codebase, err := clientcodebase.NewFromConfig(
		t.Context(),
		clientcodebase.Config{Kind: clientcodebase.KindGit, Root: root, Name: ""},
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Decorations = fixtures.Decorations()
			row.Runner = runner
		}),
	)
	require.NoError(t, err)

	testCodebaseClientGitKindIncludeIgnoredChecksListing(t, codebase)
}

// testCodebaseClientGitKindIncludeIgnoredChecksListing continues
// TestCodebaseClient_GitKindIncludeIgnored.
func testCodebaseClientGitKindIncludeIgnoredChecksListing(
	t *testing.T,
	codebase interfaces.Codebase,
) {
	t.Helper()

	files, err := codebase.Files(
		t.Context(),
		fixtures.BuildRef(func(row *types.FileSelector) {
			row.IncludeGenerated = true
		}),
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]types.File{{Path: textAGo, Untracked: false, Ignored: false}},
		files,
	)

	files, err = codebase.Files(
		t.Context(),
		fixtures.BuildRef(func(row *types.FileSelector) {
			row.Roots = []string{textOut}
			row.Globs = []string{"out/*.go"}
			row.IncludeGenerated = true
			row.IncludeIgnored = true
		}),
	)
	require.NoError(t, err)
	assert.Equal(t,
		[]types.File{{Path: textOutZzGenGo, Untracked: true, Ignored: true}}, files)

	files, err = codebase.Files(
		t.Context(),
		fixtures.BuildRef(func(row *types.FileSelector) {
			row.Paths = []string{textOutZzGenGo}
			row.IncludeGenerated = true
			row.IncludeIgnored = true
		}),
	)
	require.NoError(t, err)
	assert.Equal(t,
		[]types.File{{Path: textOutZzGenGo, Untracked: true, Ignored: true}}, files)

	files, err = codebase.Files(
		t.Context(),
		fixtures.BuildRef(func(row *types.FileSelector) {
			row.Globs = []string{"**/zz_gen.go"}
			row.IncludeGenerated = true
			row.IncludeIgnored = true
		}),
	)
	require.NoError(t, err)
	assert.Equal(t,
		[]types.File{{Path: textOutZzGenGo, Untracked: true, Ignored: true}}, files)
}

const (
	// textOutZzGenGo is the literal "out/zz_gen.go".
	textOutZzGenGo = "out/zz_gen.go"
)

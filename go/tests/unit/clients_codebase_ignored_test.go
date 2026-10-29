package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientcodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
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
		"a.go": "package a\n", "out/zz_gen.go": "package out\n", "out/log.txt": "x\n",
	})
	runner := mocks.NewMockCommandRunner(gomock.NewController(t))
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--cached").
		Return(interfaces.CmdResult{Stdout: []byte("a.go\x00")}).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--others",
		"--exclude-standard").Return(interfaces.CmdResult{}).AnyTimes()
	ignored := []byte("out/log.txt\x00out/zz_gen.go\x00")
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--others",
		"--ignored", "--exclude-standard", "--", "out").
		Return(interfaces.CmdResult{Stdout: ignored}).Times(1)
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--others",
		"--ignored", "--exclude-standard", "--", "out/zz_gen.go").
		Return(interfaces.CmdResult{Stdout: []byte("out/zz_gen.go\x00")}).Times(1)
	runner.EXPECT().RunBuffered(gomock.Any(), root, "git", "ls-files", "-z", "--others",
		"--ignored", "--exclude-standard").
		Return(interfaces.CmdResult{Stdout: ignored}).Times(1)
	cb, err := clientcodebase.NewFromConfig(t.Context(),
		clientcodebase.Config{Kind: clientcodebase.KindGit, Root: root},
		clientcodebase.Deps{Decorations: fixtures.Decorations(), Runner: runner})
	require.NoError(t, err)

	files, err := cb.Files(t.Context(), types.FileSelector{IncludeGenerated: true})
	require.NoError(t, err)
	assert.Equal(t, []types.File{{Path: "a.go"}}, files)

	files, err = cb.Files(t.Context(), types.FileSelector{
		Roots: []string{"out"}, Globs: []string{"out/*.go"},
		IncludeGenerated: true, IncludeIgnored: true,
	})
	require.NoError(t, err)
	assert.Equal(t,
		[]types.File{{Path: "out/zz_gen.go", Untracked: true, Ignored: true}}, files)

	files, err = cb.Files(t.Context(), types.FileSelector{
		Paths: []string{"out/zz_gen.go"}, IncludeGenerated: true, IncludeIgnored: true,
	})
	require.NoError(t, err)
	assert.Equal(t,
		[]types.File{{Path: "out/zz_gen.go", Untracked: true, Ignored: true}}, files)

	files, err = cb.Files(t.Context(), types.FileSelector{
		Globs: []string{"**/zz_gen.go"}, IncludeGenerated: true, IncludeIgnored: true,
	})
	require.NoError(t, err)
	assert.Equal(t,
		[]types.File{{Path: "out/zz_gen.go", Untracked: true, Ignored: true}}, files)
}

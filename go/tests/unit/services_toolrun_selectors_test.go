package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	clientcodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/toolrun/selectors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// memoryCodebase returns the memory-kind codebase over files.
func memoryCodebase(t *testing.T, files map[string]string) interfaces.Codebase {
	t.Helper()
	cb, err := clientcodebase.NewFromConfig(t.Context(), clientcodebase.Config{},
		clientcodebase.Deps{Decorations: fixtures.Decorations(), Files: files})
	require.NoError(t, err)
	return cb
}

// selectWith runs the selector kind over cb.
func selectWith(
	t *testing.T,
	kind string,
	deps selectors.Deps,
	cb interfaces.Codebase,
	spec types.TargetSpec,
) []string {
	t.Helper()
	sel, err := selectors.NewFromConfig(kind, deps)
	require.NoError(t, err)
	got, err := sel.Select(t.Context(), cb, spec)
	require.NoError(t, err)
	return got
}

// TestToolrunSelectors_Kinds tests every target selector kind.
//
// Why this test is important:
//   - The targets decide what a tool runs over: a missed module goes unlinted, and an
//     extra target (a module with no Go code, a heredoc Dockerfile) fails the run for
//     a reason unrelated to the code.
//
// What it tests:
//   - files filters by glob and drops heredoc Dockerfiles under SkipHeredoc; file_dirs
//     yields each matched file's directory once;
//     go_modules reads go.work and, under WithPackages, drops a module whose only Go
//     file belongs to a nested module; python_projects lists pyproject.toml
//     directories (root included); ts_workspace follows pnpm-workspace.yaml with an
//     exclusion and yields nothing without one; staged_files asks git with the glob.
func TestToolrunSelectors_Kinds(t *testing.T) {
	t.Parallel()
	cb := memoryCodebase(t, map[string]string{
		"go.work":  "go 1.27\n\nuse (\n\t./a\n\t./b\n\t./b/inner\n)\n",
		"a/go.mod": "module a\n", "a/a.go": "package a\n",
		"b/go.mod": "module b\n", "b/inner/go.mod": "module inner\n",
		"b/inner/i.go":               "package inner\n",
		"docker/api/Dockerfile":      "FROM x\nRUN echo hi\n",
		"docker/web/Dockerfile":      "FROM x\nRUN <<EOF\necho\nEOF\n",
		"pyproject.toml":             "[project]\n",
		"py/svc/pyproject.toml":      "[project]\n",
		"pnpm-workspace.yaml":        "packages:\n  - apps/*\n  - '!apps/old'\n",
		"apps/web/package.json":      "{}",
		"apps/old/package.json":      "{}",
		"apps/docs/README.md":        "no package\n",
		"charts/a/tests/x_test.yaml": "", "charts/a/tests/y_test.yaml": "",
		"charts/b/tests/z_test.yaml": "",
	})
	none := selectors.Deps{}

	assert.Equal(
		t,
		[]string{"docker/api/Dockerfile", "docker/web/Dockerfile"},
		selectWith(
			t,
			selectors.KindFiles,
			none,
			cb,
			types.TargetSpec{Glob: "docker/*/Dockerfile"},
		),
	)
	assert.Equal(t, []string{"docker/api/Dockerfile"},
		selectWith(t, selectors.KindFiles, none, cb,
			types.TargetSpec{Glob: "docker/*/Dockerfile", SkipHeredoc: true}))
	assert.Equal(t, []string{"charts/a/tests", "charts/b/tests"},
		selectWith(t, selectors.KindFileDirs, none, cb,
			types.TargetSpec{Glob: "charts/*/tests/*_test.yaml"}))
	assert.Equal(t, []string{"a", "b", "b/inner"},
		selectWith(t, selectors.KindGoModules, none, cb, types.TargetSpec{}))
	assert.Equal(
		t,
		[]string{"a", "b/inner"},
		selectWith(
			t,
			selectors.KindGoModules,
			none,
			cb,
			types.TargetSpec{WithPackages: true},
		),
	)
	assert.Equal(t, []string{".", "py/svc"},
		selectWith(t, selectors.KindPythonProjects, none, cb, types.TargetSpec{}))
	assert.Equal(t, []string{"apps/web"},
		selectWith(t, selectors.KindTSWorkspace, none, cb, types.TargetSpec{}))
	assert.Empty(t, selectWith(t, selectors.KindTSWorkspace, none,
		memoryCodebase(t, map[string]string{"a.ts": ""}), types.TargetSpec{}))

	git := mocks.NewMockGitOps(gomock.NewController(t))
	git.EXPECT().StagedFiles(gomock.Any(), "/repo", []string{"*.go"}).
		Return([]string{"z.go", "a.go"}, nil)
	assert.Equal(t, []string{"a.go", "z.go"},
		selectWith(t, selectors.KindStagedFiles, selectors.Deps{Git: git, Root: "/repo"},
			cb, types.TargetSpec{Glob: "*.go"}))
}

// TestToolrunSelectors_Unknown tests the factory's failures.
//
// Why this test is important:
//   - A typo in a manifest row must fail when the row is loaded, not select nothing
//     and report a clean run.
//
// What it tests:
//   - An unknown kind and staged_files without git operations are INVALID_INPUT.
func TestToolrunSelectors_Unknown(t *testing.T) {
	t.Parallel()
	_, err := selectors.NewFromConfig("go_module", selectors.Deps{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	_, err = selectors.NewFromConfig(selectors.KindStagedFiles, selectors.Deps{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestToolrunSelectors_RootsGlobsAndExclude tests the target filters every kind shares.
//
// Why this test is important:
//   - A repository scopes a linter to part of the tree (Python under its source roots,
//     shell scripts in their test dirs) and drops generated targets; without the
//     filters a row would lint generated code or miss the scope its old method had.
//
// What it tests:
//   - Globs adds patterns to a files kind's Glob (any of them matches).
//   - Roots keeps only targets at or under one of the roots.
//   - Exclude drops targets matching a glob, for every kind.
func TestToolrunSelectors_RootsGlobsAndExclude(t *testing.T) {
	t.Parallel()
	cb := memoryCodebase(t, map[string]string{
		"go.work":                        "go 1.27\n\nuse (\n\t./pkg\n\t./gen/api\n)\n",
		"pkg/go.mod":                     "module p\n",
		"gen/api/go.mod":                 "module g\n",
		"apps/python/svc/pyproject.toml": "",
		"apps/python/svc_generated/pyproject.toml": "",
		"tools/x/pyproject.toml":                   "",
		"pyproject.toml":                           "",
		"zarf/docker/tests/a.sh":                   "",
		"e2e/b.sh":                                 "",
		"e2e/deep/c.sh":                            "",
		"scripts/d.sh":                             "",
	})
	assert.Equal(t, []string{"pkg"},
		selectWith(t, "go_modules", selectors.Deps{}, cb,
			types.TargetSpec{Exclude: []string{"gen/**"}}))
	assert.Equal(t, []string{"apps/python/svc", "tools/x"},
		selectWith(t, "python_projects", selectors.Deps{}, cb, types.TargetSpec{
			Roots: []string{"apps/python", "tools"}, Exclude: []string{"**/*generated"},
		}))
	assert.Equal(t, []string{"e2e/b.sh", "zarf/docker/tests/a.sh"},
		selectWith(t, "files", selectors.Deps{}, cb, types.TargetSpec{
			Glob: "zarf/docker/tests/*.sh", Globs: []string{"e2e/*.sh"},
		}))
}

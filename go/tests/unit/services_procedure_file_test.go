package unit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/fsys"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/procedure"
)

// fileProc is a procedure of one file step per op, over the input "paths" for ensure.
func fileProc() []types.Procedure {
	return []types.Procedure{{
		Name: textFiles, Inputs: []string{textPaths, textFrom},
		Steps: []types.ProcStep{
			{Name: "mounts", Each: textPaths, File: &types.ProcFile{
				Op: types.FileEnsure, Path: "{item}",
			}},
			{Name: "secrets", File: &types.ProcFile{
				Op:   types.FileCopy,
				From: "{from}",
				Path: textConfSecYaml,
				Mode: "0600",
			}},
			{
				Name: "cache",
				File: &types.ProcFile{Op: types.FileMkdir, Path: "out/cache"},
			},
			{
				Name: textStale,
				File: &types.ProcFile{Op: types.FileRemove, Path: "out/stale"},
			},
		},
	}}
}

// write creates root/rel with data, making its directory.
func write(t *testing.T, root, rel, data string) {
	t.Helper()

	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), perm0o750))
	require.NoError(t, os.WriteFile(path, []byte(data), perm0o600))
}

// TestServicesProcedure_FileOps tests the file step's four operations under the root.
//
// Why this test is important:
//   - Bootstrapping a dev stack is file chores (a bind-mounted file Compose would
//     otherwise create as a directory, a secrets file seeded from its example, a cache
//     directory, a stale output); a procedure row must do each idempotently, never
//     clobbering what a developer already has.
//
// What it tests:
//   - ensure creates a missing file empty, keeps an existing file's content and replaces
//     the empty directory Docker left in a file's place.
//   - copy seeds a missing Path from From with Mode, and a second run leaves an edited
//     Path alone; mkdir creates the directory; remove deletes a tree, and passes again
//     once it is gone.
func TestServicesProcedure_FileOps(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, root, textConfKeptYaml, "mine")
	require.NoError(t, os.MkdirAll(filepath.Join(root, textConf, "dir.yaml"), perm0o750))
	write(t, root, "conf/secrets.yaml.example", "token: x")
	write(t, root, "out/stale/a/b.txt", "old")

	eng := procEngine(t, &recorder{}, fileProc(), &procedure.Deps{
		Root: root, FS: fsys.NewOSFS(),
	})
	vars := procedure.Vars{
		textPaths: []any{"conf/new.yaml", textConfKeptYaml, "conf/dir.yaml"},
		textFrom:  "conf/secrets.yaml.example",
	}

	res, err := eng.Run(t.Context(), textFiles, vars, types.RunOptions{})
	require.NoError(t, err)
	assert.False(t, res.HasFailures(), "%+v", res)

	for rel, want := range map[string]string{
		"conf/new.yaml": "", textConfKeptYaml: "mine", "conf/dir.yaml": "",
		textConfSecYaml: "token: x",
	} {
		got, readErr := os.ReadFile(filepath.Clean(filepath.Join(root, rel)))
		require.NoError(t, readErr, rel)
		assert.Equal(t, want, string(got), rel)
	}

	info, err := os.Stat(filepath.Join(root, textConf, "secrets.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(fileModeArg0o600), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(root, textOut, "cache"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.NoDirExists(t, filepath.Join(root, textOut, textStale))

	write(t, root, textConfSecYaml, "edited")

	res, err = eng.Run(t.Context(), textFiles, vars, types.RunOptions{})
	require.NoError(t, err)
	assert.False(t, res.HasFailures(), "%+v", res)

	got, err := os.ReadFile(filepath.Clean(filepath.Join(root, textConf, "secrets.yaml")))
	require.NoError(t, err)
	assert.Equal(t, "edited", string(got), "copy never overwrites an existing path")
}

// TestServicesProcedure_FileRefusals tests the file step's failures and dry run.
//
// Why this test is important:
//   - A file row deletes and writes under the operator's tree, so it must never escape
//     the root, never delete a directory holding work in place of a file, report a
//     missing copy source, and change nothing on a dry run.
//
// What it tests:
//   - ensure over a non-empty directory fails naming it and leaves it; copy from a
//     missing From fails; a path expanding outside the root fails.
//   - A dry run skips every file step and writes nothing.
//   - Building rejects an unknown op, a copy without From, an empty path and a mode that
//     is not octal.
func TestServicesProcedure_FileRefusals(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, root, "conf/full/keep.txt", "work")

	one := func(f types.ProcFile) types.StepResult {
		eng := procEngine(t, &recorder{}, []types.Procedure{{
			Name: textOne, Steps: []types.ProcStep{{Name: textF, File: &f}},
		}}, &procedure.Deps{Root: root, FS: fsys.NewOSFS()})
		res, err := eng.Run(t.Context(), textOne, nil, types.RunOptions{})
		require.NoError(t, err)
		require.Len(t, res, 1)

		return res[0]
	}

	full := one(types.ProcFile{Op: types.FileEnsure, Path: "conf/full"})
	assert.Equal(t, types.StatusFail, full.Status)
	assert.Contains(t, full.Error, "conf/full")
	assert.FileExists(t, filepath.Join(root, textConf, textFull, "keep.txt"))

	missing := one(
		types.ProcFile{Op: types.FileCopy, From: "conf/no.example", Path: textC},
	)
	assert.Equal(t, types.StatusFail, missing.Status)
	assert.Contains(t, missing.Error, "conf/no.example")

	escape := one(types.ProcFile{Op: types.FileEnsure, Path: "../escape.yaml"})
	assert.Equal(t, types.StatusFail, escape.Status)
	assert.Contains(t, escape.Error, "outside the root")
	assert.NoFileExists(t, filepath.Join(filepath.Dir(root), "escape.yaml"))

	testServicesProcedureFileRefusalsRest(t)
}

// testServicesProcedureFileRefusalsRest continues TestServicesProcedure_FileRefusals
// with its remaining steps.
func testServicesProcedureFileRefusalsRest(t *testing.T) {
	t.Helper()

	dry := t.TempDir()
	eng := procEngine(t, &recorder{}, fileProc(), &procedure.Deps{
		Root: dry, FS: fsys.NewOSFS(),
	})
	res, err := eng.Run(t.Context(), textFiles, procedure.Vars{
		textPaths: []any{textAYaml}, textFrom: textB,
	}, types.RunOptions{DryRun: true})
	require.NoError(t, err)

	for i := range res {
		assert.Equal(t, types.StatusSkip, res[i].Status, res[i].Name)
	}

	entries, err := os.ReadDir(dry)
	require.NoError(t, err)
	assert.Empty(t, entries)

	for name, procFile := range map[string]types.ProcFile{
		textOp:    {Op: "chmod", Path: textA},
		textFrom:  {Op: types.FileCopy, Path: textA},
		textPath2: {Op: types.FileMkdir},
		textMode:  {Op: types.FileMkdir, Path: textA, Mode: "rwx"},
		"mixed":   {Op: types.FileRemove, Path: textA, From: textB},
	} {
		_, err := procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{{
			Name: textBad, Steps: []types.ProcStep{{Name: textF, File: &procFile}},
		}}}, &procedure.Deps{})
		require.Error(t, err, name)
	}
}

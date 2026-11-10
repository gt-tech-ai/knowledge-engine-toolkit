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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// fileProc is a procedure of one file step per op, over the input "paths" for ensure.
func fileProc() []types.Procedure {
	return []types.Procedure{fixtures.Build(func(row *types.Procedure) {
		row.Name = textFiles
		row.Inputs = []string{textPaths, textFrom}
		row.Steps = []types.ProcStep{
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = "mounts"
				row.Each = textPaths
				row.File = &types.ProcFile{
					Op: types.FileEnsure, Path: "{item}",
					From: "",
					Mode: "",
				}
			}),
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = "secrets"
				row.File = &types.ProcFile{
					Op:   types.FileCopy,
					From: "{from}",
					Path: textConfSecYaml,
					Mode: "0600",
				}
			}),
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = "cache"
				row.File = &types.ProcFile{
					Op:   types.FileMkdir,
					Path: "out/cache",
					From: "",
					Mode: "",
				}
			}),
			fixtures.Build(func(row *types.ProcStep) {
				row.Name = textStale
				row.File = &types.ProcFile{
					Op:   types.FileRemove,
					Path: "out/stale",
					From: "",
					Mode: "",
				}
			}),
		}
	})}
}

// write creates root/rel with data, making its directory.
func write(t *testing.T, root, rel, data string) {
	t.Helper()

	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), mkdirAllInputWrite))
	require.NoError(t, os.WriteFile(path, []byte(data), writeFileInput))
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
	require.NoError(
		t,
		os.MkdirAll(filepath.Join(root, textConf, "dir.yaml"), mkdirAllInputOps),
	)
	write(t, root, "conf/secrets.yaml.example", "token: x")
	write(t, root, "out/stale/a/b.txt", "old")

	eng := procEngine(
		t,
		new(recorder),
		fileProc(),
		fixtures.BuildRef(func(row *procedure.Deps) {
			row.Root = root
			row.FS = fsys.NewOSFS()
		}),
	)
	vars := procedure.Vars{
		textPaths: []any{"conf/new.yaml", textConfKeptYaml, "conf/dir.yaml"},
		textFrom:  "conf/secrets.yaml.example",
	}

	res, err := eng.Run(t.Context(), textFiles, vars, stdx.Zero[types.RunOptions]())
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
	assert.Equal(t, os.FileMode(fileModeInput), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(root, textOut, "cache"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.NoDirExists(t, filepath.Join(root, textOut, textStale))

	write(t, root, textConfSecYaml, "edited")

	res, err = eng.Run(t.Context(), textFiles, vars, stdx.Zero[types.RunOptions]())
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

	one := func(procFile types.ProcFile) types.StepResult {
		eng := procEngine(t, new(recorder), []types.Procedure{
			fixtures.Build(func(target *types.Procedure) {
				target.Name = textOne
				target.Steps = []types.ProcStep{
					fixtures.Build(func(row *types.ProcStep) {
						row.Name = textF
						row.File = &procFile
					}),
				}
			}),
		}, fixtures.BuildRef(func(row *procedure.Deps) {
			row.Root = root
			row.FS = fsys.NewOSFS()
		}))
		res, err := eng.Run(t.Context(), textOne, nil, stdx.Zero[types.RunOptions]())
		require.NoError(t, err)
		require.Len(t, res, 1)

		return res[0]
	}

	full := one(
		types.ProcFile{Op: types.FileEnsure, Path: "conf/full", From: "", Mode: ""},
	)
	assert.Equal(t, types.StatusFail, full.Status)
	assert.Contains(t, full.Error, "conf/full")
	assert.FileExists(t, filepath.Join(root, textConf, textFull, "keep.txt"))

	missing := one(
		types.ProcFile{
			Op:   types.FileCopy,
			From: "conf/no.example",
			Path: textC,
			Mode: "",
		},
	)
	assert.Equal(t, types.StatusFail, missing.Status)
	assert.Contains(t, missing.Error, "conf/no.example")

	escape := one(
		types.ProcFile{Op: types.FileEnsure, Path: "../escape.yaml", From: "", Mode: ""},
	)
	assert.Equal(t, types.StatusFail, escape.Status)
	assert.Contains(t, escape.Error, "outside the root")
	assert.NoFileExists(t, filepath.Join(filepath.Dir(root), "escape.yaml"))

	testServicesProcedureFileRefusalsSkipsWithoutWriting(t)
}

// testServicesProcedureFileRefusalsSkipsWithoutWriting continues
// TestServicesProcedure_FileRefusals: skips without writing.
func testServicesProcedureFileRefusalsSkipsWithoutWriting(t *testing.T) {
	t.Helper()

	dry := t.TempDir()
	eng := procEngine(
		t,
		new(recorder),
		fileProc(),
		fixtures.BuildRef(func(row *procedure.Deps) {
			row.Root = dry
			row.FS = fsys.NewOSFS()
		}),
	)
	res, err := eng.Run(t.Context(), textFiles, procedure.Vars{
		textPaths: []any{textAYaml}, textFrom: textB,
	}, fixtures.Build(func(row *types.RunOptions) {
		row.DryRun = true
	}))
	require.NoError(t, err)

	for i := range res {
		assert.Equal(t, types.StatusSkip, res[i].Status, res[i].Name)
	}

	entries, err := os.ReadDir(dry)
	require.NoError(t, err)
	assert.Empty(t, entries)

	for name, procFile := range map[string]types.ProcFile{
		textOp:   {Op: "chmod", Path: textA, From: "", Mode: ""},
		textFrom: {Op: types.FileCopy, Path: textA, From: "", Mode: ""},
		textPathLower: fixtures.Build(func(row *types.ProcFile) {
			row.Op = types.FileMkdir
		}),
		textMode: {Op: types.FileMkdir, Path: textA, Mode: "rwx", From: ""},
		"mixed":  {Op: types.FileRemove, Path: textA, From: textB, Mode: ""},
	} {
		_, err := procedure.NewFromConfig(procedure.Config{Procedures: []types.Procedure{
			fixtures.Build(func(row *types.Procedure) {
				row.Name = textBad
				row.Steps = []types.ProcStep{
					fixtures.Build(func(row *types.ProcStep) {
						row.Name = textF
						row.File = &procFile
					}),
				}
			}),
		}}, new(procedure.Deps))
		require.Error(t, err, name)
	}
}

const (
	// mkdirAllInputOps is the perm passed to MkdirAll in TestServicesProcedure_FileOps.
	mkdirAllInputOps = 0o750
)

const (
	// textConf is the literal "conf".
	textConf = "conf"

	// textConfKeptYaml is the literal "conf/kept.yaml".
	textConfKeptYaml = "conf/kept.yaml"

	// textConfSecYaml is the literal "conf/secrets.yaml".
	textConfSecYaml = "conf/secrets.yaml"

	// textFrom is the literal "from".
	textFrom = "from"
)

package unit_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	fscodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase/fs"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/gen"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/errcodes"
)

// repoRoot is the repository root relative to this package.
const repoRoot = "../../.."

// committedTaxonomy returns the committed taxonomy source and both rendered outputs,
// keyed by their repository-relative paths.
func committedTaxonomy(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, p := range []string{errcodes.SourcePath, errcodes.GoPath, errcodes.PythonPath} {
		b, err := os.ReadFile(filepath.FromSlash(repoRoot + "/" + p))
		require.NoError(t, err)
		files[p] = string(b)
	}
	return files
}

// TestErrcodes_CommittedOutputsMatchTaxonomy tests that the committed Go and Python
// error-code files are a fresh render of the taxonomy.
//
// Why this test is important:
//   - the taxonomy is the one source of every code and its HTTP, gRPC, retry and
//     message facts; a hand-edited output, or a taxonomy edit that was not
//     regenerated, would let Go and Python disagree on the wire.
//
// What it tests:
//   - the generator renders exactly the Go and Python outputs, and gen.Check over the
//     repository reports both in sync.
func TestErrcodes_CommittedOutputsMatchTaxonomy(t *testing.T) {
	t.Parallel()
	cb := codebase.New(
		fscodebase.New(filepath.FromSlash(repoRoot)),
		codebase.NewSnapshot("errcodes"),
		nil,
	)

	out, err := errcodes.New().Render(t.Context(), cb)
	require.NoError(t, err)
	require.ElementsMatch(
		t,
		[]string{errcodes.GoPath, errcodes.PythonPath},
		slices.Collect(maps.Keys(out)),
	)

	d, err := gen.Check(t.Context(), cb, errcodes.New())

	require.NoError(t, err)
	require.True(t, d.InSync, "run `go generate ./go/core/errors/...`:\n%s", d.Diff)
}

// TestErrcodes_HandEditIsDrift tests that editing a generated output by hand is drift.
//
// Why this test is important:
//   - a code changed in one language's output only is exactly the cross-language
//     mismatch the generator exists to prevent; the drift check must name the file.
//
// What it tests:
//   - a code value edited in the Go output, or a code added to the Python output,
//     makes gen.Check report that one path out of sync.
func TestErrcodes_HandEditIsDrift(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, path, from, to string
	}{
		{
			name: "go value", path: errcodes.GoPath,
			from: `CodeNotFound ErrorCode = "NOT_FOUND"`,
			to:   `CodeNotFound ErrorCode = "MISSING"`,
		},
		{
			name: "python code", path: errcodes.PythonPath,
			from: `    NOT_FOUND = "NOT_FOUND"`,
			to:   "    NOT_FOUND = \"NOT_FOUND\"\n    GONE = \"GONE\"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := committedTaxonomy(t)
			require.Contains(t, files[tc.path], tc.from)
			files[tc.path] = strings.Replace(files[tc.path], tc.from, tc.to, 1)

			d, err := gen.Check(t.Context(), memoryCodebase(t, files), errcodes.New())

			require.NoError(t, err)
			require.False(t, d.InSync)
			require.Equal(t, []string{tc.path}, d.Paths)
		})
	}
}

// TestErrcodes_RejectsInvalidTaxonomy tests that a malformed taxonomy fails to render.
//
// Why this test is important:
//   - a typo in a retry class or a gRPC status name would otherwise render a
//     silently wrong classification into both languages.
//
// What it tests:
//   - an unknown retry class, an unknown gRPC status, a duplicate code, an unknown
//     field and a row missing its Go name each fail Render with CodeInvalidInput.
func TestErrcodes_RejectsInvalidTaxonomy(t *testing.T) {
	t.Parallel()
	row := func(fields string) string {
		return "codes:\n  - code: A\n    go: CodeA\n    doc: a\n    http: 500\n" + fields
	}
	cases := map[string]string{
		"retry class": row("    grpc: INTERNAL\n    retry: sometimes\n"),
		"grpc status": row("    grpc: TEAPOT\n    retry: none\n"),
		"duplicate": row("    grpc: INTERNAL\n    retry: none\n") +
			"  - {code: A, go: CodeB, doc: b, http: 500, grpc: INTERNAL, retry: none}\n",
		"unknown field": row("    grpc: INTERNAL\n    retry: none\n    colour: red\n"),
		"missing go": "codes:\n  - {code: A, doc: a, http: 500, grpc: INTERNAL," +
			" retry: none}\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cb := memoryCodebase(t, map[string]string{errcodes.SourcePath: src})

			_, err := errcodes.New().Render(t.Context(), cb)

			require.Error(t, err)
			require.True(t, apperr.Is(err, apperr.CodeInvalidInput), err.Error())
		})
	}
}

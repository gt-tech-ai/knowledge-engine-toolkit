package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/file"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestFileSource_PutThenGetRoundTripsOnIgnoredPath tests that the file Source writes a
// credential to a git-ignored file and reads the same value back.
//
// Why this test is important:
//   - file is the read/write backend for locally-provisioned credentials; a broken
//     write→read roundtrip would silently drop or corrupt a stored credential.
//
// What it tests:
//   - After git check-ignore confirms the path is ignored (runner returns nil), Put
//     persists the value and Get reveals it.
func TestFileSource_PutThenGetRoundTripsOnIgnoredPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	// `git check-ignore -q <path>` exits 0 when the path IS ignored → Run returns nil.
	runner.EXPECT().
		Run(gomock.Any(), gomock.Any(), "git", "check-ignore", "-q", gomock.Any()).
		Return(nil)

	path := filepath.Join(t.TempDir(), ".secrets.env")
	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	src := file.New(
		map[types.Ref]file.Location{ref: {Path: path, Key: "CLIENT_SECRET"}},
		runner,
	)

	require.NoError(t, src.Put(context.Background(), ref, types.NewSecret("wrote-me")))

	got, err := src.Get(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, "wrote-me", got.Reveal())
}

// TestFileSource_PutRefusesNonIgnoredPath tests that the file Source refuses to write a
// credential to a path git does not ignore, and writes nothing.
//
// Why this test is important:
//   - This is the guard that prevents a secret from being committed. If Put wrote to a
//     tracked path, the next commit would leak the credential — the exact failure the
//     backend exists to prevent.
//
// What it tests:
//   - When git check-ignore reports not-ignored (runner returns an error), Put returns
//     CodeInvalidInput and no file is created.
func TestFileSource_PutRefusesNonIgnoredPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	// Not ignored → check-ignore exits 1 → the CommandRunner returns a non-nil error.
	runner.EXPECT().
		Run(gomock.Any(), gomock.Any(), "git", "check-ignore", "-q", gomock.Any()).
		Return(apperr.Sentinel("exit status 1"))

	path := filepath.Join(t.TempDir(), "tracked.env")
	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	src := file.New(
		map[types.Ref]file.Location{ref: {Path: path, Key: "CLIENT_SECRET"}},
		runner,
	)

	err := src.Put(context.Background(), ref, types.NewSecret("must-not-write"))
	require.True(t, apperr.Is(err, apperr.CodeInvalidInput))

	_, statErr := os.Stat(path)
	assert.True(
		t,
		os.IsNotExist(statErr),
		"must not create a non-git-ignored credential file",
	)
}

// TestFileSource_GetMissingKeyIsNotFound tests that Get for a key absent from the file
// yields a coded not-found error.
//
// Why this test is important:
//   - A missing key must be an explicit not-found, not an empty Secret a caller could
//     mistake for a real credential.
//
// What it tests:
//   - Get for a Ref whose key is not present in the file returns CodeNotFound (and Get
//     never invokes the runner).
func TestFileSource_GetMissingKeyIsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".secrets.env")
	require.NoError(t, os.WriteFile(path, []byte("OTHER=x\n"), 0o600))

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl) // Get must not call the runner.
	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	src := file.New(
		map[types.Ref]file.Location{ref: {Path: path, Key: "CLIENT_SECRET"}},
		runner,
	)

	_, err := src.Get(context.Background(), ref)
	require.True(t, apperr.Is(err, apperr.CodeNotFound))
}

// TestFileSource_ReadsExportAndQuotedDotenv tests that the file Source reads a shell-
// sourceable dotenv file — `export KEY="value"` — returning the unquoted credential.
//
// Why this test is important:
//   - The git-ignored per-tenant credential files are maintained for BOTH shell `source`
//     and this backend, so they use the shell `export KEY="value"` form. If the parser
//     matched the key as `export KEY` or returned the value with its quotes, every real
//     credential would resolve as not-found or fail the anti-mixing tenant guard — the
//     exact failure seen in the field.
//
// What it tests:
//   - `export CLIENT_SECRET="the-secret"` and a single-quoted domain both parse to the
//     bare value; a plain `KEY=value` line still works; and a Put preserves the `export `
//     prefix.
func TestFileSource_ReadsExportAndQuotedDotenv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".secrets.env")
	require.NoError(t, os.WriteFile(path, []byte(
		"export CLIENT_SECRET=\"the-secret\"\n"+
			"export DOMAIN='dev.example.com'\n"+
			"PLAIN=bare-value\n",
	), 0o600))

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl) // Get must not call the runner.
	sec := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	dom := types.Ref{Env: "dev", Class: "bootstrap", Field: "domain"}
	plain := types.Ref{Env: "dev", Class: "bootstrap", Field: "plain"}
	src := file.New(map[types.Ref]file.Location{
		sec:   {Path: path, Key: "CLIENT_SECRET"},
		dom:   {Path: path, Key: "DOMAIN"},
		plain: {Path: path, Key: "PLAIN"},
	}, runner)

	got, err := src.Get(context.Background(), sec)
	require.NoError(t, err)
	assert.Equal(t, "the-secret", got.Reveal(), "export + double quotes stripped")

	got, err = src.Get(context.Background(), dom)
	require.NoError(t, err)
	assert.Equal(t, "dev.example.com", got.Reveal(), "export + single quotes stripped")

	got, err = src.Get(context.Background(), plain)
	require.NoError(t, err)
	assert.Equal(t, "bare-value", got.Reveal(), "plain KEY=value still parses")

	// A Put on the export-format file preserves the `export ` prefix (still sourceable).
	runner.EXPECT().
		Run(gomock.Any(), gomock.Any(), "git", "check-ignore", "-q", gomock.Any()).
		Return(nil)
	require.NoError(t, src.Put(context.Background(), sec, types.NewSecret("rotated")))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "export CLIENT_SECRET=rotated",
		"a rotated export-format key keeps its export prefix")
}

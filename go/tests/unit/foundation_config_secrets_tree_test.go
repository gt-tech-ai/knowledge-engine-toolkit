package unit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Literals the secret-tree tests share.
const (
	secretsDirName = "secrets.d"
	dbPasswordKey  = "database.password"
	dbPasswordRel  = "database/password"
	dbDir          = "database"
	secretDirPerm  = 0o750
	unreadableMode = 0o000
)

// writeSecretFile writes one secrets.d credential file at dir/secrets.d/<rel>,
// creating parent directories, for the secret-tree tests.
func writeSecretFile(t *testing.T, dir, rel, content string) {
	t.Helper()

	path := filepath.Join(dir, secretsDirName, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), secretDirPerm))
	writeFile(t, path, content)
}

// baseForSecretTree writes a minimal base.yaml (so the loader has a base layer) in a
// fresh temp dir and returns the dir, for the secret-tree tests.
func baseForSecretTree(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, textBaseYaml), "database:\n  host: \"localhost\"\n")

	return dir
}

// TestSecretTree_MergesKeyPerFile tests that each regular file under secrets.d/ is
// merged at the config key its path names, with one trailing newline trimmed.
//
// Why this test is important:
//   - BYOC workloads receive their credentials as a key-per-file tree projected from a
//     Secret; a credential file beside base.yaml is useless unless the loader
//     reads it, and a value with a stray trailing newline would authenticate as wrong.
//
// What it tests:
//   - secrets.d/database/password sets database.password and secrets.d/redis/password
//     sets redis.password, each with its file's trailing "\n" removed.
func TestSecretTree_MergesKeyPerFile(t *testing.T) {
	t.Parallel()

	dir := baseForSecretTree(t)
	writeSecretFile(t, dir, dbPasswordRel, "s3cret\n")
	writeSecretFile(t, dir, "redis/password", "r3dis")

	cfg := loadWithSchema(t, dir)

	assert.Equal(t, "s3cret", cfg.GetString(dbPasswordKey))
	assert.Equal(t, "r3dis", cfg.GetString("redis.password"))
}

// TestSecretTree_EnvVarWins tests that an environment variable still overrides a value
// the secrets.d tree sets, so Compose and local runs behave as before.
//
// Why this test is important:
//   - The tree is a layer below the env bindings; if it merged over them, a local
//     developer's env override would stop working the moment a file existed.
//
// What it tests:
//   - With both secrets.d/database/password and MYAPP_DATABASE_PASSWORD set, the env
//     value wins.
func TestSecretTree_EnvVarWins(t *testing.T) {
	dir := baseForSecretTree(t)
	writeSecretFile(t, dir, dbPasswordRel, "from-file")
	t.Setenv("MYAPP_DATABASE_PASSWORD", "from-env")

	cfg := loadWithSchema(t, dir)

	assert.Equal(t, "from-env", cfg.GetString(dbPasswordKey))
}

// TestSecretTree_MissingDirIsNoLayer tests that a config dir with no secrets.d/ loads
// without error and keeps the lower layers' values.
//
// Why this test is important:
//   - Compose and local runs have no secrets.d; the new layer must be strictly additive
//     and never fail a boot that worked before.
//
// What it tests:
//   - With no secrets.d/, Load succeeds and database.host keeps its base.yaml value.
func TestSecretTree_MissingDirIsNoLayer(t *testing.T) {
	t.Parallel()

	dir := baseForSecretTree(t)

	cfg := loadWithSchema(t, dir)

	assert.Equal(t, "localhost", cfg.GetString("database.host"))
}

// TestSecretTree_ResolvesProjectedSymlink tests that a secrets.d that is a symlink into
// a Kubernetes projected volume's ..data directory is resolved and walked.
//
// Why this test is important:
//   - A projected Secret volume's top-level entries are symlinks into kubelet's ..data
//     dir; walking the symlink itself as a file would misread every credential.
//
// What it tests:
//   - With secrets.d -> ..data (a real dir holding database/password), the value merges.
func TestSecretTree_ResolvesProjectedSymlink(t *testing.T) {
	t.Parallel()

	dir := baseForSecretTree(t)
	data := filepath.Join(dir, "..data")
	require.NoError(t, os.MkdirAll(filepath.Join(data, dbDir), secretDirPerm))
	writeFile(t, filepath.Join(data, dbDir, "password"), "linked\n")
	require.NoError(t, os.Symlink("..data", filepath.Join(dir, "secrets.d")))

	cfg := loadWithSchema(t, dir)

	assert.Equal(t, "linked", cfg.GetString(dbPasswordKey))
}

// TestSecretTree_UnreadableFileFailsCoded tests that an unreadable credential file fails
// the boot with a coded internal error rather than a silent empty value.
//
// Why this test is important:
//   - A credential the loader cannot read must stop the process loudly; booting with an
//     empty password would fail far from the cause, or worse, authenticate as a default.
//
// What it tests:
//   - A secrets.d/database/password with mode 0000 makes config.New return an error whose
//     apperr code is CodeInternal.
func TestSecretTree_UnreadableFileFailsCoded(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root bypasses file mode bits")
	}

	dir := baseForSecretTree(t)
	writeSecretFile(t, dir, dbPasswordRel, "s3cret")
	require.NoError(
		t,
		os.Chmod(filepath.Join(dir, secretsDirName, dbDir, "password"), unreadableMode),
	)

	_, err := config.New(
		config.KindViper,
		[]options.Option[config.Config]{
			config.WithBaseDir(dir),
			config.WithEnvironment(""),
			config.WithEnvPrefix(textMyapp),
			config.WithSchema(new(bindingSchema)),
		}...,
	)

	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

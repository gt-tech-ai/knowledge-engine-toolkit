package unit_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/stepcache"
)

// TestFoundationStepcache_Key tests the content-addressed key.
//
// Why this test is important:
//   - A key that ignores an input, the tool version or an argument would skip a step
//     whose result could differ, and report a pass nobody ran.
//
// What it tests:
//   - The key is stable for equal fields and changes when any input byte, the version
//     or an argument changes, including when bytes move between fields.
func TestFoundationStepcache_Key(t *testing.T) {
	t.Parallel()
	base := stepcache.Key([]byte("src"), "v1", []string{"run", "./..."})
	assert.Len(t, base, 64)
	assert.Equal(t, base, stepcache.Key([]byte("src"), "v1", []string{"run", "./..."}))
	for name, other := range map[string]string{
		"input":   stepcache.Key([]byte("srd"), "v1", []string{"run", "./..."}),
		"version": stepcache.Key([]byte("src"), "v2", []string{"run", "./..."}),
		"arg":     stepcache.Key([]byte("src"), "v1", []string{"run", "./x"}),
		"split":   stepcache.Key([]byte("src"), "v1", []string{"ru", "n./..."}),
	} {
		assert.NotEqual(t, base, other, name)
	}
}

// TestFoundationStepcache_Kinds tests the off and dir kinds and the factory.
//
// Why this test is important:
//   - The default must never skip a step; the dir kind must remember a pass across
//     runs and never write outside its directory.
//
// What it tests:
//   - off never hits; dir hits a recorded key from a fresh cache over the same
//     directory, misses another, records twice without error and rejects a malformed
//     key; an unknown kind and a dir kind with no directory are INVALID_INPUT.
func TestFoundationStepcache_Kinds(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	key := stepcache.Key([]byte("a"), "v", nil)

	off, err := stepcache.NewFromConfig(stepcache.Config{})
	require.NoError(t, err)
	require.NoError(t, off.Record(ctx, key))
	hit, err := off.Hit(ctx, key)
	require.NoError(t, err)
	assert.False(t, hit)

	root := filepath.Join(t.TempDir(), "cache")
	cfg := stepcache.Config{Kind: stepcache.KindDir, Dir: root}
	c, err := stepcache.NewFromConfig(cfg)
	require.NoError(t, err)
	require.NoError(t, c.Record(ctx, key))
	require.NoError(t, c.Record(ctx, key))
	again, err := stepcache.NewFromConfig(cfg)
	require.NoError(t, err)
	hit, err = again.Hit(ctx, key)
	require.NoError(t, err)
	assert.True(t, hit)
	hit, err = again.Hit(ctx, stepcache.Key([]byte("b"), "v", nil))
	require.NoError(t, err)
	assert.False(t, hit)
	_, err = again.Hit(ctx, "../../etc/passwd")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = stepcache.NewFromConfig(stepcache.Config{Kind: "redis"})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	_, err = stepcache.NewFromConfig(stepcache.Config{Kind: stepcache.KindDir})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestFoundationStepcache_SaltPartitionsKeys tests that a salted dir cache only answers
// for keys recorded under the same salt.
//
// Why this test is important:
//   - a key covers a step's inputs and definition, not the code that judges them; a
//     rebuilt checker (a guard whose logic changed) must not trust the passes its
//     predecessor recorded, so the caller salts the cache with the checker's build.
//
// What it tests:
//   - a key recorded under salt "a" hits under "a", misses under "b" and unsalted;
//     BinarySalt is a stable hex SHA-256 of the running executable.
func TestFoundationStepcache_SaltPartitionsKeys(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root := t.TempDir()
	key := stepcache.Key([]byte("a"), "v", nil)
	open := func(salt string) interfaces.StepCache {
		c, err := stepcache.NewFromConfig(stepcache.Config{
			Kind: stepcache.KindDir, Dir: root, Salt: salt,
		})
		require.NoError(t, err)
		return c
	}
	require.NoError(t, open("a").Record(ctx, key))
	for salt, want := range map[string]bool{"a": true, "b": false, "": false} {
		hit, err := open(salt).Hit(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, want, hit, "salt %q", salt)
	}

	salt, err := stepcache.BinarySalt()
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{64}$`, salt)
	again, err := stepcache.BinarySalt()
	require.NoError(t, err)
	assert.Equal(t, salt, again)
}

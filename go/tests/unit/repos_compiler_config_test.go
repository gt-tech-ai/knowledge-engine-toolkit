package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reposcfg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/repos"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository/compiler"
)

// TestCompilerConfig_Validate tests the repos.compiler.kind config validation.
//
// Why this test is important:
//   - The config selects the query-compiler backend; accepting an unimplemented kind
//     would fail opaquely at query time instead of loudly at config load. Validate is the
//     loud-failure gate.
//
// What it tests:
//   - The default kind is "ent" and validates; an unimplemented/unknown kind is rejected.
func TestCompilerConfig_Validate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "ent", reposcfg.DefaultCompilerConfig().Kind)
	require.NoError(t, reposcfg.CompilerConfig{Kind: "ent"}.Validate())
	require.Error(t, reposcfg.CompilerConfig{Kind: "mongo"}.Validate())
	assert.Error(t, reposcfg.CompilerConfig{Kind: ""}.Validate())
}

// TestCompilerNew_SelectsBackendByKind tests the composition-root backend selection.
//
// Why this test is important:
//   - New is the single selection point that maps a configured Kind to a backend; it must
//     return the Ent backend for the implemented kind and fail loudly (coded error) for a
//     reserved one, never silently return nil.
//
// What it tests:
//   - ParseKind round-trips the config token; New(KindEnt) yields a usable backend; a
//     reserved kind (KindMongo) returns a coded error.
func TestCompilerNew_SelectsBackendByKind(t *testing.T) {
	t.Parallel()

	kind, err := compiler.ParseKind("ent")
	require.NoError(t, err)
	assert.Equal(t, compiler.KindEnt, kind)

	_, err = compiler.ParseKind("bogus")
	require.Error(t, err)

	backend, err := compiler.New(compiler.KindEnt)
	require.NoError(t, err)
	require.NotNil(t, backend)
	assert.NotNil(t, backend.Empty()) // the backend is usable

	_, err = compiler.New(compiler.KindMongo)
	assert.Error(t, err, "a reserved kind fails loudly")
}

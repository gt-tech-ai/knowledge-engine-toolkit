package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestNewFromConfig_EnvKindResolves tests that the factory builds a working env Source
// from config.
//
// Why this test is important:
//   - The factory is the single wiring entrypoint; if KindEnv is misrouted, no consumer
//     can read an environment-backed credential.
//
// What it tests:
//   - NewFromConfig(KindEnv) returns a Source whose Get resolves the mapped variable.
func TestNewFromConfig_EnvKindResolves(t *testing.T) {
	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	t.Setenv("KE_FACTORY_ENV_TEST", "from-env")

	src, err := secrets.NewFromConfig(context.Background(), secrets.Config{
		Kind:    secrets.KindEnv,
		EnvVars: map[types.Ref]string{ref: "KE_FACTORY_ENV_TEST"},
	}, secrets.Deps{})
	require.NoError(t, err)

	got, err := src.Get(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, "from-env", got.Reveal())
}

// TestNewFromConfig_FileKindRequiresRunner tests that the factory fails loudly when the
// file backend is selected without its CommandRunner dependency.
//
// Why this test is important:
//   - The file backend's git-ignore write guard cannot function without a runner; a nil
//     runner must fail at wiring time, not with a nil-panic on the first Put.
//
// What it tests:
//   - NewFromConfig(KindFile) with no Runner returns CodeInvalidInput.
func TestNewFromConfig_FileKindRequiresRunner(t *testing.T) {
	_, err := secrets.NewFromConfig(
		context.Background(),
		secrets.Config{Kind: secrets.KindFile},
		secrets.Deps{},
	)
	require.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}

// TestNewFromConfig_FileKindBuildsWithRunner tests that the factory builds a file Source
// when a runner is supplied.
//
// Why this test is important:
//   - The happy path must succeed so the file backend is reachable by config selection.
//
// What it tests:
//   - NewFromConfig(KindFile) with a Runner returns a non-nil Source and no error.
func TestNewFromConfig_FileKindBuildsWithRunner(t *testing.T) {
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)

	src, err := secrets.NewFromConfig(
		context.Background(),
		secrets.Config{Kind: secrets.KindFile},
		secrets.Deps{Runner: runner},
	)
	require.NoError(t, err)
	require.NotNil(t, src)
}

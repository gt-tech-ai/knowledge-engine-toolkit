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
	})
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

	src, err := secrets.NewFromConfig(context.Background(), secrets.Config{
		Kind:   secrets.KindFile,
		Runner: runner,
	})
	require.NoError(t, err)
	require.NotNil(t, src)
}

// TestNewFromConfig_UnknownKindFailsLoudly tests that an unrecognized Kind is rejected.
//
// Why this test is important:
//   - Fail-loud on an unknown Kind is the factory contract
//     (ARCHITECTURE.md#swappable-components); a silent nil Source would defer the failure
//     to a confusing later nil-panic.
//
// What it tests:
//   - NewFromConfig with an out-of-range Kind returns CodeInvalidInput.
func TestNewFromConfig_UnknownKindFailsLoudly(t *testing.T) {
	_, err := secrets.NewFromConfig(
		context.Background(),
		secrets.Config{Kind: secrets.Kind(99)},
	)
	require.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}

// TestKind_String tests that each Kind renders its stable config string.
//
// Why this test is important:
//   - The Kind strings are the config vocabulary a product manifest selects a backend by;
//     a drift here silently breaks config-driven selection.
//
// What it tests:
//   - KindEnv/KindFile/KindAWSSM render "env"/"file"/"aws-sm".
func TestKind_String(t *testing.T) {
	assert.Equal(t, "env", secrets.KindEnv.String())
	assert.Equal(t, "file", secrets.KindFile.String())
	assert.Equal(t, "aws-sm", secrets.KindAWSSM.String())
}

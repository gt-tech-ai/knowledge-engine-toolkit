package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/env"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TestEnvSource_ResolvesMappedVariable tests that the env credential Source reads the
// value of the environment variable its Ref maps to.
//
// Why this test is important:
//   - env is the read path for CI/CD and container-injected credentials; if the Ref→var
//     mapping is not honored, every consumer reads the wrong (or no) credential.
//
// What it tests:
//   - Get on a mapped Ref returns a Secret revealing the mapped variable's value.
func TestEnvSource_ResolvesMappedVariable(t *testing.T) {
	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	t.Setenv("KE_DEV_BOOTSTRAP_CLIENT_SECRET", "s3cr3t")

	src := env.New(map[types.Ref]string{ref: "KE_DEV_BOOTSTRAP_CLIENT_SECRET"})

	got, err := src.Get(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", got.Reveal())
}

// TestEnvSource_MissingAndUnmappedAreNotFound tests that an unmapped Ref and a mapped but
// unset variable both yield a coded not-found error, never a silent empty Secret.
//
// Why this test is important:
//   - A missing credential must be distinguishable from an empty one, so a caller never
//     injects "" believing it read a real value.
//
// What it tests:
//   - Get on an unmapped Ref, and on a Ref mapped to an unset variable, both return
//     CodeNotFound.
func TestEnvSource_MissingAndUnmappedAreNotFound(t *testing.T) {
	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}

	unmapped := env.New(map[types.Ref]string{})
	_, err := unmapped.Get(context.Background(), ref)
	require.True(
		t,
		apperr.Is(err, apperr.CodeNotFound),
		"unmapped ref must be not-found",
	)

	unset := env.New(map[types.Ref]string{ref: "KE_DEFINITELY_UNSET_VAR_XYZ_9137"})
	_, err = unset.Get(context.Background(), ref)
	require.True(
		t,
		apperr.Is(err, apperr.CodeNotFound),
		"unset variable must be not-found",
	)
}

// TestEnvSource_PutIsUnsupported tests that writing to the env Source fails loudly.
//
// Why this test is important:
//   - Environment variables are set out-of-band; a silent no-op Put would lose a written
//     credential. The read-only contract must be enforced with a coded error.
//
// What it tests:
//   - Put returns CodeInvalidInput (unsupported) rather than succeeding silently.
func TestEnvSource_PutIsUnsupported(t *testing.T) {
	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "client_secret"}
	src := env.New(map[types.Ref]string{ref: "KE_X"})

	err := src.Put(context.Background(), ref, types.NewSecret("v"))
	require.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}

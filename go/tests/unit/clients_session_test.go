package unit_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/session"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// sessionCred is a test credential.
type sessionCred struct {
	// Key is the access key.
	Key string `json:"key"`
}

// TestClientsSession_ResolveVerifyCache tests the session provider.
//
// Why this test is important:
//   - Every credentialed command opens a session first; resolving or verifying on
//     every call costs a network round trip each, and a credential that fails
//     verification must stop the command with a clear auth error, not run with it.
//
// What it tests:
//   - Concurrent first resolutions read and verify once, later ones hit the cache, and
//     Env projects the credential; a failed verification is UNAUTHORIZED and is
//     retried on the next call; an unparsable secret is INVALID_INPUT.
func TestClientsSession_ResolveVerifyCache(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	src := mocks.NewMockSource(gomock.NewController(t))
	ref := types.Ref{Env: textStg, Class: textDeploy, Field: "aws"}
	src.EXPECT().
		Get(gomock.Any(), ref).
		Return(types.NewSecret(`{"key":"AK"}`), nil).
		Times(1)

	verified := 0

	testClientsSessionResolveVerifyCacheRest6(ctx, t, src, ref, verified)
}

// testClientsSessionResolveVerifyCacheRest6 continues
// testClientsSessionResolveVerifyCacheRest5 with its remaining steps.
func testClientsSessionResolveVerifyCacheRest6(
	ctx context.Context, t *testing.T, src *mocks.MockSource, ref types.Ref, verified int,
) {
	t.Helper()

	var lock sync.Mutex

	provider := session.NewProvider(src,
		func(b []byte) (sessionCred, error) {
			var sessionCredValue sessionCred

			err := json.Unmarshal(b, &sessionCredValue)
			if err != nil {
				return sessionCredValue, apperr.Wrap(
					err,
					apperr.CodeInvalidInput,
					"parse",
				)
			}

			return sessionCredValue, nil
		},
		func(_ context.Context, sessionCred2 sessionCred) error {
			lock.Lock()
			defer lock.Unlock()

			verified++

			if sessionCred2.Key == "BAD" {
				return apperr.New(apperr.CodeUpstream, "denied")
			}

			return nil
		},
		func(c sessionCred) []string { return []string{"AWS_ACCESS_KEY_ID=" + c.Key} })

	testClientsSessionResolveVerifyCacheRest2(ctx, t, src, ref, &verified, provider)
}

// testClientsSessionResolveVerifyCacheRest2 continues
// testClientsSessionResolveVerifyCacheRest6 with its remaining steps.
func testClientsSessionResolveVerifyCacheRest2(
	ctx context.Context,
	t *testing.T,
	src *mocks.MockSource,
	ref types.Ref,
	verified *int,
	provider *session.Provider[sessionCred],
) {
	t.Helper()

	var group sync.WaitGroup

	for range iterations8 {
		group.Go(func() {
			c, err := provider.Resolve(ctx, ref)
			if assert.NoError(
				t,
				err,
			) {
				assert.Equal(t, "AK", c.Key)
			}
		})
	}

	group.Wait()

	env, err := provider.Env(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, []string{"AWS_ACCESS_KEY_ID=AK"}, env)
	assert.Equal(t, 1, (*verified))

	testClientsSessionResolveVerifyCacheRest(ctx, t, src, provider)
}

// testClientsSessionResolveVerifyCacheRest continues
// TestClientsSession_ResolveVerifyCache with its remaining steps.
func testClientsSessionResolveVerifyCacheRest(
	ctx context.Context,
	t *testing.T,
	src *mocks.MockSource,
	provider *session.Provider[sessionCred],
) {
	t.Helper()

	var err error

	bad := types.Ref{Env: textStg, Class: textDeploy, Field: textBad}
	src.EXPECT().
		Get(gomock.Any(), bad).
		Return(types.NewSecret(`{"key":"BAD"}`), nil).
		Times(timesArg2)

	for range iterations2 {
		_, err = provider.Resolve(ctx, bad)
		assert.Equal(t, apperr.CodeUnauthorized, apperr.Code(err))
	}

	junk := types.Ref{Env: textStg, Class: textDeploy, Field: "junk"}
	src.EXPECT().Get(gomock.Any(), junk).Return(types.NewSecret("not json"), nil)

	_, err = provider.Resolve(ctx, junk)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

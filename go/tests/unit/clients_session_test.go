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

	testClientsSessionResolveVerifyCacheBuildsProvider(ctx, t, src, ref, verified)
}

// testClientsSessionResolveVerifyCacheBuildsProvider continues
// testClientsSessionResolveVerifyCache: builds provider.
func testClientsSessionResolveVerifyCacheBuildsProvider(
	ctx context.Context, t *testing.T, src *mocks.MockSource, ref types.Ref, verified int,
) {
	t.Helper()

	var lock sync.Mutex

	provider := session.NewProvider(src,
		func(b []byte) (sessionCred, error) {
			var currentSessionCred sessionCred

			err := json.Unmarshal(b, &currentSessionCred)
			if err != nil {
				return currentSessionCred, apperr.Wrap(
					err,
					apperr.CodeInvalidInput,
					"parse",
				)
			}

			return currentSessionCred, nil
		},
		func(_ context.Context, secondSessionCred sessionCred) error {
			lock.Lock()
			defer lock.Unlock()

			verified++

			if secondSessionCred.Key == "BAD" {
				return apperr.New(apperr.CodeUpstream, "denied")
			}

			return nil
		},
		func(c sessionCred) []string { return []string{"AWS_ACCESS_KEY_ID=" + c.Key} })

	testClientsSessionResolveVerifyCacheCachesAndExports(
		ctx,
		t,
		src,
		ref,
		&verified,
		provider,
	)
}

// testClientsSessionResolveVerifyCacheCachesAndExports continues
// testClientsSessionResolveVerifyCacheBuildsProvider: caches and exports.
func testClientsSessionResolveVerifyCacheCachesAndExports(
	ctx context.Context,
	t *testing.T,
	src *mocks.MockSource,
	ref types.Ref,
	verified *int,
	provider *session.Provider[sessionCred],
) {
	t.Helper()

	var group sync.WaitGroup

	for range iterationsExports {
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

	testClientsSessionResolveVerifyCacheRejectsBadCredentials(ctx, t, src, provider)
}

// testClientsSessionResolveVerifyCacheRejectsBadCredentials continues
// TestClientsSession_ResolveVerifyCache: rejects bad credentials.
func testClientsSessionResolveVerifyCacheRejectsBadCredentials(
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
		Times(wantCalls)

	for range iterations {
		_, err = provider.Resolve(ctx, bad)
		assert.Equal(t, apperr.CodeUnauthorized, apperr.Code(err))
	}

	junk := types.Ref{Env: textStg, Class: textDeploy, Field: "junk"}
	src.EXPECT().Get(gomock.Any(), junk).Return(types.NewSecret("not json"), nil)

	_, err = provider.Resolve(ctx, junk)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

const (
	// iterationsExports is the iteration count in
	// testClientsSessionResolveVerifyCacheCachesAndExports.
	iterationsExports = 8
)

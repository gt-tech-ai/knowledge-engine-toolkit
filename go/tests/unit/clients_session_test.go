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
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
	ok := types.Ref{Env: "stg", Class: "deploy", Field: "aws"}
	src.EXPECT().
		Get(gomock.Any(), ok).
		Return(types.NewSecret(`{"key":"AK"}`), nil).
		Times(1)
	verified := 0
	var mu sync.Mutex
	p := session.NewProvider(src,
		func(b []byte) (sessionCred, error) {
			var c sessionCred
			if err := json.Unmarshal(b, &c); err != nil {
				return c, apperr.Wrap(err, apperr.CodeInvalidInput, "parse")
			}
			return c, nil
		},
		func(_ context.Context, c sessionCred) error {
			mu.Lock()
			defer mu.Unlock()
			verified++
			if c.Key == "BAD" {
				return apperr.New(apperr.CodeUpstream, "denied")
			}
			return nil
		},
		func(c sessionCred) []string { return []string{"AWS_ACCESS_KEY_ID=" + c.Key} })

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			c, err := p.Resolve(ctx, ok)
			if assert.NoError(
				t,
				err,
			) {
				assert.Equal(t, "AK", c.Key)
			}
		})
	}
	wg.Wait()
	env, err := p.Env(ctx, ok)
	require.NoError(t, err)
	assert.Equal(t, []string{"AWS_ACCESS_KEY_ID=AK"}, env)
	assert.Equal(t, 1, verified)

	bad := types.Ref{Env: "stg", Class: "deploy", Field: "bad"}
	src.EXPECT().
		Get(gomock.Any(), bad).
		Return(types.NewSecret(`{"key":"BAD"}`), nil).
		Times(2)
	for range 2 {
		_, err = p.Resolve(ctx, bad)
		assert.Equal(t, apperr.CodeUnauthorized, apperr.Code(err))
	}

	junk := types.Ref{Env: "stg", Class: "deploy", Field: "junk"}
	src.EXPECT().Get(gomock.Any(), junk).Return(types.NewSecret("not json"), nil)
	_, err = p.Resolve(ctx, junk)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

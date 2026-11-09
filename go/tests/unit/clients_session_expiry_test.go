package unit_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/session"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// expiringCred is a test credential that reports when it expires.
type expiringCred struct {
	// Exp is the credential's expiry; zero never expires.
	Exp time.Time `json:"exp"`
}

// ExpiresAt reports the credential's expiry.
func (c expiringCred) ExpiresAt() time.Time { return c.Exp }

// expiryProvider returns a provider over src parsing T from JSON, with a no-op verify.
func expiryProvider[T any](src *mocks.MockSource) *session.Provider[T] {
	return session.NewProvider(src,
		func(b []byte) (T, error) {
			var value T

			err := json.Unmarshal(b, &value)
			if err != nil {
				return value, apperr.Wrap(err, apperr.CodeInvalidInput, "parse")
			}

			return value, nil
		},
		func(context.Context, T) error { return nil },
		func(T) []string { return nil })
}

// TestProvider_ExpiryAwareCache tests that the session cache never serves an expired
// credential.
//
// Why this test is important:
//   - A long-lived process (an MCP server) resolves a session once and reuses it; a
//     cached SSO credential that expired an hour ago fails every later call with an
//     auth error the operator can't fix by retrying.
//
// What it tests:
//   - A far-future Expiring credential is served from the cache (one source read over
//     two Resolves); one already inside ExpirySkew is fetched again on each Resolve; a
//     zero expiry and a non-Expiring credential are cached.
func TestProvider_ExpiryAwareCache(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	ref := types.Ref{Env: textStg, Class: textDeploy, Field: "aws"}
	secretAt := func(exp time.Time) types.Secret {
		b, err := json.Marshal(expiringCred{Exp: exp})
		require.NoError(t, err)

		return types.NewSecret(string(b))
	}

	cases := []struct {
		exp   time.Time
		name  string
		reads int
	}{
		{time.Now().Add(time.Hour), "far future is cached", 1},
		{
			time.Now().Add(session.ExpirySkew / 2),
			"inside the skew is refetched",
			casesCache,
		},
		{time.Time{}, "zero never expires", 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			src := mocks.NewMockSource(gomock.NewController(t))
			src.EXPECT().
				Get(gomock.Any(), ref).
				Return(secretAt(testCase.exp), nil).
				Times(testCase.reads)

			p := expiryProvider[expiringCred](src)
			for range 2 {
				_, err := p.Resolve(ctx, ref)
				require.NoError(t, err)
			}
		})
	}

	plain := mocks.NewMockSource(gomock.NewController(t))
	plain.EXPECT().
		Get(gomock.Any(), ref).
		Return(types.NewSecret(`{"key":"AK"}`), nil).
		Times(1)

	p := expiryProvider[sessionCred](plain)
	for range iterations {
		_, err := p.Resolve(ctx, ref)
		require.NoError(t, err)
	}
}

const (
	// casesCache is the cases cache the tests use.
	casesCache = 2
)

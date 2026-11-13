package unit_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	connectorgh "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/github"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// ghAppID and ghInstallationID are the App and installation the auth tests mint for.
const (
	ghAppID          = 123
	ghInstallationID = 42
	ghKeyBits        = 2048
	ghTokenLife      = time.Hour
	ghJWTParts       = 3
	ghFirstToken     = "ghs_first"
	ghSecondToken    = "ghs_second"
	ghRepeatCalls    = 2
	ghClockUnix      = 1794571200 // 2026-11-13T12:00:00Z
)

// ghClock is the auth tests' fixed clock start.
func ghClock() time.Time {
	return time.Unix(ghClockUnix, 0).UTC()
}

// ghKey generates an App private key and returns it with its PKCS#1 PEM.
func ghKey(t *testing.T) (*rsa.PrivateKey, interfaces.Secret) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, ghKeyBits)
	require.NoError(t, err)

	block := &pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   x509.MarshalPKCS1PrivateKey(key),
	}

	return key, interfaces.Secret(pem.EncodeToMemory(block))
}

// ghTokenReply is a 201 access_tokens reply carrying token, expiring at expires.
func ghTokenReply(token string, expires time.Time) *http.Response {
	return promResponse(http.StatusCreated,
		`{"token":"`+token+`","expires_at":"`+expires.Format(time.RFC3339)+`"}`)
}

// ghVerifyJWT checks the request's App JWT: RS256 over header.claims with key, issued
// by the App, and valid for at most ten minutes.
func ghVerifyJWT(t *testing.T, req *http.Request, key *rsa.PrivateKey) {
	t.Helper()

	jwt, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	require.True(t, ok)

	parts := strings.Split(jwt, ".")
	require.Len(t, parts, ghJWTParts)

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	require.NoError(t, err)

	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	require.NoError(t, rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig))

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)

	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}

	require.NoError(t, json.Unmarshal(payload, &claims))
	assert.Equal(t, "123", claims.Iss)
	assert.LessOrEqual(t, claims.Exp-claims.Iat, int64((10 * time.Minute).Seconds()))
}

// ghAuth builds an AppAuth over doer whose clock reads *now.
func ghAuth(
	t *testing.T,
	doer interfaces.HTTPDoer,
	pemKey interfaces.Secret,
	now *time.Time,
) *connectorgh.AppAuth {
	t.Helper()

	auth, err := connectorgh.NewAppAuth(connectorgh.AppAuthConfig{
		BaseURL:        "https://ghe.example/api/v3/",
		PrivateKeyPEM:  pemKey,
		AppID:          ghAppID,
		InstallationID: ghInstallationID,
	}, doer, func() time.Time { return *now })
	require.NoError(t, err)

	return auth
}

// TestAuth_ReusesTokenUntilFiveMinutesBeforeExpiry tests that an installation token is
// minted once and reused while it has more than five minutes left.
//
// Why this test is important:
//   - GitHub limits App token exchanges; minting one per page would exhaust the limit
//     on a large repository and slow every page by a round trip.
//
// What it tests:
//   - The first Token POSTs a signed RS256 App JWT (issuer = the App id, at most ten
//     minutes long) to <base>/app/installations/<id>/access_tokens with the pinned API
//     version; a second Token one second before the five-minute window reuses the
//     token without another request.
func TestAuth_ReusesTokenUntilFiveMinutesBeforeExpiry(t *testing.T) {
	t.Parallel()

	key, pemKey := ghKey(t)
	doer := mocks.NewMockHTTPDoer(gomock.NewController(t))
	now := ghClock()

	doer.EXPECT().
		Do(gomock.Any()).
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodPost, req.Method)
			assert.Equal(t,
				"https://ghe.example/api/v3/app/installations/42/access_tokens",
				req.URL.String())
			assert.Equal(t, "2022-11-28", req.Header.Get("X-Github-Api-Version"))
			ghVerifyJWT(t, req, key)

			return ghTokenReply(ghFirstToken, ghClock().Add(ghTokenLife)), nil
		})

	auth := ghAuth(t, doer, pemKey, &now)

	got, err := auth.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, ghFirstToken, got)

	now = ghClock().Add(ghTokenLife - 5*time.Minute - time.Second)

	got, err = auth.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, ghFirstToken, got)
}

// TestAuth_RefreshesNearExpiry tests that a token inside its last five minutes is
// replaced by a fresh exchange.
//
// Why this test is important:
//   - A crawl lasts longer than a token; reusing one to its last second would fail the
//     page whose requests straddle the expiry.
//
// What it tests:
//   - At exactly five minutes before expiry, Token exchanges a new JWT and returns the
//     new token, and keeps returning it.
func TestAuth_RefreshesNearExpiry(t *testing.T) {
	t.Parallel()

	_, pemKey := ghKey(t)
	doer := mocks.NewMockHTTPDoer(gomock.NewController(t))
	now := ghClock()

	gomock.InOrder(
		doer.EXPECT().Do(gomock.Any()).
			DoAndReturn(func(*http.Request) (*http.Response, error) {
				return ghTokenReply(ghFirstToken, ghClock().Add(ghTokenLife)), nil
			}),
		doer.EXPECT().Do(gomock.Any()).
			DoAndReturn(func(*http.Request) (*http.Response, error) {
				return ghTokenReply(ghSecondToken, ghClock().Add(2*ghTokenLife)), nil
			}),
	)

	auth := ghAuth(t, doer, pemKey, &now)

	_, err := auth.Token(t.Context())
	require.NoError(t, err)

	now = ghClock().Add(ghTokenLife - 5*time.Minute)

	for range ghRepeatCalls {
		got, err := auth.Token(t.Context())
		require.NoError(t, err)
		assert.Equal(t, ghSecondToken, got)
	}
}

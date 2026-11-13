package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// refreshBefore is how long before its expiry a cached installation token is replaced,
// so a token never expires during a page's requests.
const refreshBefore = 5 * time.Minute

// jwtLifetime is the App JWT's lifetime; GitHub refuses more than ten minutes.
const jwtLifetime = 9 * time.Minute

// jwtBackdate backdates the App JWT's issued-at against clock drift, as GitHub advises.
const jwtBackdate = 60 * time.Second

// decimal is the base the App and installation ids are formatted in.
const decimal = 10

// TokenSource yields the installation access token every API request sends.
type TokenSource interface {
	// Token returns a token valid for at least a few more minutes, or a coded error.
	Token(ctx context.Context) (string, error)
}

// AppAuthConfig names the GitHub App installation an AppAuth mints tokens for.
type AppAuthConfig struct {
	// BaseURL is the REST API root; empty means DefaultBaseURL.
	BaseURL string

	// PrivateKeyPEM is the App's PEM-encoded RSA private key (PKCS#1 or PKCS#8).
	PrivateKeyPEM interfaces.Secret

	// AppID is the App's numeric id, the JWT issuer.
	AppID int64

	// InstallationID is the installation the tokens are minted for.
	InstallationID int64
}

// AppAuth is the TokenSource for one App installation: it signs an RS256 App JWT,
// exchanges it at POST /app/installations/{id}/access_tokens, and reuses the token
// until five minutes before it expires. Safe for concurrent use; concurrent callers
// share one exchange.
type AppAuth struct {
	// now reads the clock.
	now func() time.Time

	// key signs the App JWT.
	key *rsa.PrivateKey

	// token is the cached installation token ("" before the first exchange).
	token string

	// expires is when token expires.
	expires time.Time

	// api sends the exchange.
	api api

	// path is the exchange's path.
	path string

	// issuer is the App id as the JWT iss claim.
	issuer string

	// mu guards token and expires, and serializes exchanges.
	mu sync.Mutex
}

// Compile-time assertion that AppAuth satisfies TokenSource.
var _ TokenSource = (*AppAuth)(nil)

// NewAppAuth returns the AppAuth for cfg, sending through doer and reading the clock
// with now. It does no I/O; an unparsable key or a non-positive id is CodeInvalidInput.
func NewAppAuth(
	cfg AppAuthConfig,
	doer interfaces.HTTPDoer,
	now func() time.Time,
) (*AppAuth, error) {
	if cfg.AppID <= 0 || cfg.InstallationID <= 0 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"github: the App id and the installation id are required",
		)
	}

	key, err := parseKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}

	installation := strconv.FormatInt(cfg.InstallationID, decimal)

	return &AppAuth{
		now:     now,
		key:     key,
		token:   "",
		expires: time.Time{},
		api:     newAPI(doer, cfg.BaseURL),
		path:    "/app/installations/" + installation + "/access_tokens",
		issuer:  strconv.FormatInt(cfg.AppID, decimal),
		mu:      sync.Mutex{},
	}, nil
}

// accessToken is the access_tokens reply.
type accessToken struct {
	// ExpiresAt is when the token expires.
	ExpiresAt time.Time `json:"expires_at"`

	// Token is the installation token.
	Token string `json:"token"`
}

// Token returns the cached installation token, exchanging a fresh App JWT for a new
// one when none is cached or the cached one expires within five minutes.
func (a *AppAuth) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := a.now()
	if a.token != "" && now.Before(a.expires.Add(-refreshBefore)) {
		return a.token, nil
	}

	jwt, err := a.appJWT(now)
	if err != nil {
		return "", err
	}

	var got accessToken

	err = a.api.call(
		ctx,
		http.MethodPost,
		a.path,
		"Bearer "+jwt,
		http.StatusCreated,
		&got,
	)
	if err != nil {
		return "", err
	}

	a.token, a.expires = got.Token, got.ExpiresAt

	return a.token, nil
}

// jwtClaims is the App JWT's payload.
type jwtClaims struct {
	// Issuer is the App id.
	Issuer string `json:"iss"`

	// IssuedAt is the backdated issue time, in Unix seconds.
	IssuedAt int64 `json:"iat"`

	// ExpiresAt is the expiry, in Unix seconds.
	ExpiresAt int64 `json:"exp"`
}

// appJWT signs the App JWT issued at now.
func (a *AppAuth) appJWT(now time.Time) (string, error) {
	claims, err := json.Marshal(jwtClaims{
		Issuer:    a.issuer,
		IssuedAt:  now.Add(-jwtBackdate).Unix(),
		ExpiresAt: now.Add(jwtLifetime).Unix(),
	})
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "github: encode JWT claims")
	}

	enc := base64.RawURLEncoding
	signed := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." +
		enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signed))

	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "github: sign the App JWT")
	}

	return signed + "." + enc.EncodeToString(sig), nil
}

// parseKey decodes a PEM RSA private key in PKCS#1 (GitHub's download) or PKCS#8.
func parseKey(pemKey interfaces.Secret) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"github: the App private key is not PEM",
		)
	}

	pkcs1, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err == nil {
		return pkcs1, nil
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"github: parse the App private key",
		)
	}

	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"github: the App private key is not RSA",
		)
	}

	return key, nil
}

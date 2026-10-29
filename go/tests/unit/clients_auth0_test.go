package unit_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/auth0"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// auth0Tenant is an httptest TLS server answering the Auth0 endpoints the management
// kind calls, counting the token grants it serves.
type auth0Tenant struct {
	// server is the TLS server; its host:port is the tenant domain.
	server *httptest.Server

	// tokens counts the client-credentials grants.
	tokens atomic.Int32
}

// newAuth0Tenant serves a tenant with orgs (paged two per page), a JWKS whose only
// signing key carries x5c, and the client credentials id/secret.
func newAuth0Tenant(t *testing.T, orgs []string, x5c string) *auth0Tenant {
	t.Helper()
	tenant := &auth0Tenant{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("client_id") != "id" ||
			r.PostForm.Get("client_secret") != "secret" ||
			r.PostForm.Get("grant_type") != "client_credentials" ||
			r.PostForm.Get("audience") != "https://"+r.Host+"/api/v2/" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		tenant.tokens.Add(1)
		_ = json.NewEncoder(w).
			Encode(map[string]any{"access_token": "tok", "expires_in": 3600})
	})
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc(
		"GET /api/v2/organizations",
		func(w http.ResponseWriter, r *http.Request) {
			if !authorized(w, r) {
				return
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			out := []map[string]string{}
			for i := page * 2; i < len(orgs) && i < page*2+2; i++ {
				out = append(out, map[string]string{"id": orgs[i]})
			}
			_ = json.NewEncoder(w).Encode(out)
		},
	)
	mux.HandleFunc("GET /api/v2/organizations/name/{name}",
		func(w http.ResponseWriter, r *http.Request) {
			if !authorized(w, r) {
				return
			}
			if r.PathValue("name") != "acme" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "org_acme"})
		})
	mux.HandleFunc(
		"GET /.well-known/jwks.json",
		func(w http.ResponseWriter, _ *http.Request) {
			keys := []map[string]any{{"use": "enc", "kid": "enc-1", "x5c": []string{x5c}}}
			if x5c != "" {
				keys = append(
					keys,
					map[string]any{"use": "sig", "kid": "sig-1", "x5c": []string{x5c}},
				)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
		},
	)
	tenant.server = httptest.NewTLSServer(mux)
	t.Cleanup(tenant.server.Close)
	return tenant
}

// client builds the management kind against the tenant with clientID/secret.
func (a *auth0Tenant) client(
	t *testing.T,
	clientID, secret string,
) interfaces.Auth0Management {
	t.Helper()
	c, err := auth0.NewFromConfig(t.Context(), auth0.Config{
		Kind:   auth0.KindManagement,
		Domain: strings.TrimPrefix(a.server.URL, "https://"),
	}, auth0.Deps{
		Decorations: fixtures.Decorations(), Doer: a.server.Client(),
		ClientID: clientID, ClientSecret: secret,
	})
	require.NoError(t, err)
	return c
}

// auth0Cert returns a base64 DER self-signed certificate, the form a JWKS x5c carries.
func auth0Cert(t *testing.T) (x5c string, key *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "tenant"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(der), key
}

// TestClientsAuth0_Management tests the Auth0 Management API kind against a tenant.
//
// Why this test is important:
//   - A drift check compares the tenant's organizations with a registry, and a dev seed
//     renders the tenant's signing key into the gateway's config: a page dropped, a key
//     mis-selected or a grant repeated per call breaks both silently.
//
// What it tests:
//   - OrganizationIDs pages until an empty page and returns every id in order.
//   - OrganizationID returns a named org's id; an unknown name is NOT_FOUND.
//   - One client-credentials grant serves every Management call of a client.
//   - SigningKey returns the "sig" key's public key as PEM, with its kid, and needs no
//     credentials.
//   - A Management call without credentials is INVALID_INPUT; a refused grant is
//     UPSTREAM.
func TestClientsAuth0_Management(t *testing.T) {
	t.Parallel()
	x5c, key := auth0Cert(t)
	tenant := newAuth0Tenant(t, []string{"org_a", "org_b", "org_c"}, x5c)
	c := tenant.client(t, "id", "secret")
	ctx := t.Context()

	ids, err := c.OrganizationIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"org_a", "org_b", "org_c"}, ids)

	id, err := c.OrganizationID(ctx, "acme")
	require.NoError(t, err)
	assert.Equal(t, "org_acme", id)

	_, err = c.OrganizationID(ctx, "ghost")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
	assert.Equal(t, int32(1), tenant.tokens.Load(), "one grant per client")

	sk, err := tenant.client(t, "", "").SigningKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, "sig-1", sk.KeyID)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	assert.Contains(t, sk.PublicKeyPEM, "-----BEGIN PUBLIC KEY-----")
	assert.Contains(t, strings.ReplaceAll(sk.PublicKeyPEM, "\n", ""),
		base64.StdEncoding.EncodeToString(der)[:40])

	_, err = tenant.client(t, "", "").OrganizationIDs(ctx)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = tenant.client(t, "id", "wrong").OrganizationIDs(ctx)
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))
}

// TestClientsAuth0_Kinds tests the auth0 family's kind selection.
//
// Why this test is important:
//   - The stub is the default so a graph boots with no tenant, and it must refuse rather
//     than answer: an empty organization list would read as a tenant with no orgs.
//
// What it tests:
//   - The default kind's calls are UNAVAILABLE.
//   - A JWKS with no signing key fails SigningKey.
//   - The management kind without a domain, and an unknown kind, are INVALID_INPUT.
func TestClientsAuth0_Kinds(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	stub, err := auth0.NewFromConfig(ctx, auth0.Config{},
		auth0.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)
	_, err = stub.OrganizationIDs(ctx)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
	_, err = stub.SigningKey(ctx)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))

	_, err = newAuth0Tenant(t, nil, "").client(t, "", "").SigningKey(ctx)
	require.Error(t, err)

	_, err = auth0.NewFromConfig(ctx, auth0.Config{Kind: auth0.KindManagement},
		auth0.Deps{Decorations: fixtures.Decorations()})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	_, err = auth0.NewFromConfig(ctx, auth0.Config{Kind: "nope"},
		auth0.Deps{Decorations: fixtures.Decorations()})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

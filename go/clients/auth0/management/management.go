// Package management is the auth0 family's Management API backend: a client-credentials
// token, minted once and reused until it nears expiry, then the organizations endpoints;
// and the tenant's public JWKS, which needs no token.
package management

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// pageSize is the organizations page size, the Management API's maximum.
const pageSize = 100

// tokenSkew is how long before its expiry a token is minted again.
const tokenSkew = time.Minute

// Client reads one tenant.
type Client struct {
	// expires is when token stops being served (zero: never).
	expires time.Time

	// http sends every request.
	http interfaces.HTTPDoer

	// domain is the tenant domain (e.g. example.us.auth0.com).
	domain string

	// clientID and clientSecret are the Management API M2M credentials.
	clientID, clientSecret string

	// token is the cached Management API access token.
	token string

	// mu guards token and expires.
	mu sync.Mutex
}

// Compile-time assertion that Client satisfies Auth0Management.
var _ interfaces.Auth0Management = (*Client)(nil)

// New returns the client for domain over doer. Empty credentials leave only SigningKey
// usable. It does no I/O.
func New(doer interfaces.HTTPDoer, domain, clientID, clientSecret string) *Client {
	return &Client{
		http:         doer,
		domain:       domain,
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

// OrganizationIDs pages the tenant's organizations until an empty page.
func (c *Client) OrganizationIDs(ctx context.Context) ([]string, error) {
	token, err := c.bearer(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for page := 0; ; page++ {
		var orgs []struct {
			ID string `json:"id"`
		}
		endpoint := fmt.Sprintf("%s/organizations?per_page=%d&page=%d",
			c.api(), pageSize, page)
		if err := c.getJSON(ctx, endpoint, token, &orgs); err != nil {
			return nil, err
		}
		if len(orgs) == 0 {
			return ids, nil
		}
		for _, o := range orgs {
			ids = append(ids, o.ID)
		}
	}
}

// OrganizationID looks up the organization named name.
func (c *Client) OrganizationID(ctx context.Context, name string) (string, error) {
	token, err := c.bearer(ctx)
	if err != nil {
		return "", err
	}
	var org struct {
		ID string `json:"id"`
	}
	endpoint := c.api() + "/organizations/name/" + url.PathEscape(name)
	if err := c.getJSON(ctx, endpoint, token, &org); err != nil {
		return "", err
	}
	if org.ID == "" {
		return "", apperr.New(apperr.CodeUpstream,
			fmt.Sprintf("auth0: no id in the response for organization %q", name))
	}
	return org.ID, nil
}

// SigningKey reads the tenant's JWKS and returns its first "sig" key that carries an
// x5c certificate, the certificate's public key re-encoded as PEM.
func (c *Client) SigningKey(ctx context.Context) (types.SigningKey, error) {
	var jwks struct {
		Keys []struct {
			Use string   `json:"use"`
			Kid string   `json:"kid"`
			X5C []string `json:"x5c"`
		} `json:"keys"`
	}
	if err := c.getJSON(
		ctx,
		"https://"+c.domain+"/.well-known/jwks.json",
		"",
		&jwks,
	); err != nil {
		return types.SigningKey{}, err
	}
	for _, k := range jwks.Keys {
		if k.Use == "sig" && len(k.X5C) > 0 {
			pemKey, err := x5cToPEM(k.X5C[0])
			if err != nil {
				return types.SigningKey{}, err
			}
			return types.SigningKey{PublicKeyPEM: pemKey, KeyID: k.Kid}, nil
		}
	}
	return types.SigningKey{}, apperr.New(apperr.CodeUpstream,
		"auth0: the tenant's JWKS has no signing key with a certificate")
}

// api is the Management API base URL.
func (c *Client) api() string { return "https://" + c.domain + "/api/v2" }

// bearer returns the cached token, minting one when there is none or it nears expiry.
func (c *Client) bearer(ctx context.Context) (string, error) {
	if c.clientID == "" || c.clientSecret == "" {
		return "", apperr.New(apperr.CodeInvalidInput,
			"auth0: Management API calls need a client id and secret")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && (c.expires.IsZero() || time.Now().Before(c.expires)) {
		return c.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"audience":      {c.api() + "/"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+c.domain+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "auth0: build the token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := c.send(req, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", apperr.New(
			apperr.CodeUpstream,
			"auth0: the token response had no access_token",
		)
	}
	c.token, c.expires = out.AccessToken, time.Time{}
	if out.ExpiresIn > 0 {
		c.expires = time.Now().Add(time.Duration(out.ExpiresIn)*time.Second - tokenSkew)
	}
	return c.token, nil
}

// getJSON GETs endpoint, with token as the bearer when set, into out.
func (c *Client) getJSON(ctx context.Context, endpoint, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "auth0: build the request")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.send(req, out)
}

// send runs req and decodes a 2xx JSON body into out: a 404 is CodeNotFound, any other
// non-2xx status CodeUpstream carrying the start of the body.
func (c *Client) send(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUpstream, "auth0: "+req.URL.Path)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		code := apperr.CodeUpstream
		if resp.StatusCode == http.StatusNotFound {
			code = apperr.CodeNotFound
		}
		return apperr.New(code, fmt.Sprintf("auth0: %s returned HTTP %d: %s",
			req.URL.Path, resp.StatusCode, strings.TrimSpace(string(snippet))))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return apperr.Wrap(err, apperr.CodeUpstream, "auth0: decode "+req.URL.Path)
	}
	return nil
}

// x5cToPEM decodes a base64 DER certificate and re-encodes its public key as a PEM
// "PUBLIC KEY" block.
func x5cToPEM(x5c string) (string, error) {
	der, err := base64.StdEncoding.DecodeString(x5c)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeUpstream, "auth0: decode the JWKS x5c")
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return "", apperr.Wrap(
			err,
			apperr.CodeUpstream,
			"auth0: parse the JWKS certificate",
		)
	}
	pub, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeUpstream, "auth0: marshal the public key")
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})), nil
}

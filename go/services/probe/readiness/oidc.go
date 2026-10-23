package readiness

import (
	"context"
	"net/http"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// OIDC checks that an OpenID Connect tenant (an Auth0 or other identity-provider
// domain) serves its discovery document, so it exists and is configured.
type OIDC struct {
	// HTTP sends the discovery GET.
	HTTP interfaces.HTTPDoer

	// Domain is the tenant host (e.g. "acme.us.auth0.com").
	Domain string
}

// Compile-time assertion that OIDC is a readiness probe.
var _ interfaces.Probe[Target] = OIDC{}

// Name is "oidc".
func (OIDC) Name() string { return "oidc" }

// Run fetches https://<Domain>/.well-known/openid-configuration: a 200 passes; a
// transport error or any other status fails.
func (p OIDC) Run(ctx context.Context, _ Target) types.Verdict {
	url := "https://" + p.Domain + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fail(p.Name(), "build discovery request: "+err.Error())
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return fail(p.Name(), "OIDC discovery GET "+url+" failed: "+err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fail(p.Name(), "OIDC discovery "+url+" returned "+resp.Status)
	}
	return pass("OIDC tenant " + p.Domain + " serves discovery")
}

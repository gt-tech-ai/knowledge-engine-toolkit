package infra

import apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"

// OIDCConfig holds the OpenID Connect provider parameters a service validates end-user
// tokens against (all non-secret).
type OIDCConfig struct {
	// Issuer is the provider's issuer URL (the JWT iss claim), e.g.
	// "https://idp.example.com/".
	Issuer string `mapstructure:"issuer"`

	// Audience is the API resource identifier the tokens must be issued for (the aud claim).
	Audience string `mapstructure:"audience"`

	// ClientID is this service's client id at the provider, when it calls the provider
	// itself.
	ClientID string `mapstructure:"client_id"`
}

// AuthConfig holds authentication configuration for Go services.
//
// Field order is tuned for struct packing (govet fieldalignment): the large
// fields (the OIDC struct, the map, the string) lead and the three bool flags
// trail. The mapstructure/envalias tags — not declaration order — drive decoding,
// so this ordering is purely a layout concern, not a semantic one.
type AuthConfig struct {
	// OIDC holds the identity-provider parameters used when Stub is false.
	OIDC OIDCConfig `mapstructure:"oidc"`

	// ServiceTokens maps a calling service name to its service-to-service bearer
	// token. The internal WithServiceAuth interceptor validates the caller's
	// `authorization: Bearer <token>` against this per-caller map, so the server
	// records which service called (audit) and can enforce least privilege. Empty in
	// local dev (ServiceStub bypasses validation); provisioned per caller by the
	// deployment (e.g. from a secret store). A value may carry a comma-separated
	// {current,previous} pair to allow a zero-downtime rotation window. The deploy
	// delivers the whole map as one flat "caller=token;caller=token" env var (a
	// Kubernetes secretKeyRef can only carry a scalar); the loader's
	// StringToStringMapHookFunc decodes that into the map. Entries are ";"-separated
	// so a value's rotation-pair "," is preserved (e.g. "reports=cur,prev").
	ServiceTokens map[string]string `mapstructure:"service_tokens" envalias:"SERVICE_AUTH_TOKENS"`

	// ServiceToken is THIS service's own outgoing service-to-service credential — the
	// bearer token it presents when calling another internal service,
	// distinct from ServiceTokens (the callers this service accepts). Empty in local dev
	// (sends no credential); provisioned per service by the deployment.
	ServiceToken string `mapstructure:"service_token" envalias:"SERVICE_AUTH_TOKEN"`

	// Stub enables stub auth validation for local development; must never be true in
	// production.
	Stub bool `mapstructure:"stub" envalias:"STUB_AUTH"`

	// ServiceStub bypasses service-to-service auth for local development (dev
	// bypass), mirroring Stub: the internal interceptor injects a synthetic caller and
	// skips token validation so a local dev stack works without provisioned tokens. Must be
	// false in a real deployment (enforced by Validate when Stub is false).
	ServiceStub bool `mapstructure:"service_stub" envalias:"SERVICE_STUB_AUTH"`

	// ServiceAuthAudit runs the service-auth interceptor in audit mode: the caller is
	// resolved and logged but a missing/invalid token is NOT rejected. Used for a staged
	// rollout (deploy in audit, confirm every client sends a valid token, then flip to
	// enforce). Default false (enforce).
	ServiceAuthAudit bool `mapstructure:"service_auth_audit"`
}

// DefaultAuthConfig returns a safe local-dev default with both the end-user and the
// service-to-service auth stubs enabled (no identity provider or service tokens required
// locally).
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{Stub: true, ServiceStub: true}
}

// Validate returns an error when stub=false but the required OIDC params are missing, or
// when the service-auth dev bypass is left on in a real (non-stub) deployment.
func (c *AuthConfig) Validate() error {
	// A real deployment (end-user auth enforced) must not leave service-to-service auth
	// bypassed — that would leave the internal surface unauthenticated.
	if !c.Stub && c.ServiceStub {
		return apperr.InvalidInput(
			"auth.service_stub must be false when auth.stub is false",
		)
	}
	if c.Stub {
		return nil
	}
	if c.OIDC.Issuer == "" {
		return apperr.InvalidInput("auth.oidc.issuer is required when stub=false")
	}
	if c.OIDC.Audience == "" {
		return apperr.InvalidInput("auth.oidc.audience is required when stub=false")
	}
	return nil
}

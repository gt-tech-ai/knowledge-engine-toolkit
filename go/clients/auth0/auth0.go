// Package auth0 builds the Auth0Management client from config: the stub (the default),
// which reaches no tenant, or the Management API (management), run through the client
// tier (Decorate). The concrete backends live in the stub/ and management/ subpackages.
package auth0

import (
	"context"
	"net/http"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/auth0/management"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/auth0/stub"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub reaches no tenant (the default): every call is CodeUnavailable.
	KindStub = client.KindStub

	// KindManagement reads the tenant through the Management API and its JWKS.
	KindManagement client.Kind = "management"
)

// DefaultTimeout bounds each request of the http.Client KindManagement builds when no
// Doer is injected.
const DefaultTimeout = 15 * time.Second

// Config selects and configures the auth0 client. It holds no credential: those are
// runtime Deps, never config.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("auth0").
	Name string `yaml:"name" mapstructure:"name"`

	// Domain is the tenant domain (e.g. example.us.auth0.com); KindManagement needs it.
	Domain string `yaml:"domain" mapstructure:"domain"`

	// Timeout bounds each request when no Doer is injected; zero is DefaultTimeout.
	Timeout time.Duration `yaml:"timeout" mapstructure:"timeout"`
}

// Deps carries the client's runtime collaborators.
type Deps struct {
	// Decorations decorates the client and supplies its observability.
	decorate.Decorations

	// Doer sends the requests; nil is an http.Client bounded by Config.Timeout.
	Doer interfaces.HTTPDoer

	// ClientID is the Management API M2M client id; empty leaves only SigningKey usable.
	ClientID string

	// ClientSecret is the M2M client secret.
	ClientSecret string
}

// kinds is the auth0 backend table.
func kinds() client.Kinds[Config, Deps, interfaces.Auth0Management] {
	return client.Kinds[Config, Deps, interfaces.Auth0Management]{
		Family:   "auth0",
		Default:  KindStub,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.Auth0Management]{
			KindStub: func(context.Context, Config, Deps) (interfaces.Auth0Management, error) {
				return stub.Client{}, nil
			},
			KindManagement: newManagement,
		},
	}
}

// newManagement builds the Management API backend; it needs a domain.
func newManagement(
	_ context.Context,
	cfg Config,
	deps Deps,
) (interfaces.Auth0Management, error) {
	if cfg.Domain == "" {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"auth0: domain is required for the management kind")
	}
	doer := deps.Doer
	if doer == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		doer = &http.Client{Timeout: timeout}
	}
	return management.New(doer, cfg.Domain, deps.ClientID, deps.ClientSecret), nil
}

// NewFromConfig builds the client cfg.Kind selects and runs it through the client tier,
// without I/O. An unknown kind, or KindManagement without a domain, is
// CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.Auth0Management, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}

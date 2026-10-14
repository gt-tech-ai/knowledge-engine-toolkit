// Package prometheus builds the MetricsQuerier from config: the zero-infrastructure
// stub (the default) or the HTTP query API, its requests run through the client tier.
// The concrete backends live in the stub/ and http/ subpackages.
package prometheus

import (
	"context"
	nethttp "net/http"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	promhttp "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prometheus/http"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prometheus/stub"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub answers every query with a zero (the default).
	KindStub = client.KindStub

	// KindHTTP queries a Prometheus server's HTTP API.
	KindHTTP client.Kind = "http"
)

// DefaultMaxBodyBytes is DefaultConfig's response-body cap (32 MiB).
const DefaultMaxBodyBytes int64 = 32 << 20

// Config selects and configures the metrics querier.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("prometheus").
	Name string `yaml:"name" mapstructure:"name"`

	// HTTP configures KindHTTP.
	HTTP promhttp.Config `yaml:"http" mapstructure:"http"`
}

// Deps carries the querier's runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config (decoration.clients)
	// and supplies its observability.
	decorate.Decorations

	// Doer replaces the http.Client KindHTTP builds from Config.HTTP.Timeout.
	Doer promhttp.HTTPDoer
}

// DefaultConfig returns the stub kind, a 30-second request timeout and a 32 MiB body
// cap.
func DefaultConfig() Config {
	return Config{
		HTTP: promhttp.Config{
			Timeout:      30 * time.Second,
			MaxBodyBytes: DefaultMaxBodyBytes,
		},
	}
}

// kinds is the querier backend table.
func kinds() client.Kinds[Config, Deps, interfaces.MetricsQuerier] {
	return client.Kinds[Config, Deps, interfaces.MetricsQuerier]{
		Family:  "prometheus",
		Default: KindStub,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.MetricsQuerier]{
			KindStub: func(context.Context, Config, Deps) (interfaces.MetricsQuerier, error) {
				return stub.New(), nil
			},
			KindHTTP: newHTTP,
		},
	}
}

// newHTTP builds the HTTP querier, every request it sends run through the client tier.
func newHTTP(
	_ context.Context,
	cfg Config,
	deps Deps,
) (interfaces.MetricsQuerier, error) {
	if cfg.HTTP.BaseURL == "" {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"prometheus: http.base_url is required for the http kind",
		)
	}
	name := cfg.Name
	if name == "" {
		name = "prometheus"
	}
	tier, err := deps.Deps(client.Tier(), name)
	if err != nil {
		return nil, err
	}
	doer := deps.Doer
	if doer == nil {
		doer = &nethttp.Client{Timeout: cfg.HTTP.Timeout}
	}
	return promhttp.New(cfg.HTTP.BaseURL, promhttp.DecorateDoer(
		doer, client.NewBase(name, tier), cfg.HTTP.MaxBodyBytes,
	)), nil
}

// NewFromConfig builds the querier cfg.Kind selects, without I/O. An unknown kind, or
// KindHTTP without a base URL, is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.MetricsQuerier, error) {
	return kinds().New(ctx, cfg.Kind, cfg, deps)
}

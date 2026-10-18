// Package decoration is the config surface for every decorated component —
// controllers, clients, workflows, pipelines, services, repositories, stores, jobs and
// gates. Each tier has default Layers and per-component overrides keyed by the
// component's dotted name; an override applies to its name and every name beneath it
// ("api" covers "api.documents.create"), the deepest winning. Every layer (recovery,
// tracing, metrics, logging, auth, timeout, retry, circuit breaker, bulkhead, rate
// limit) is switched and tuned here, while its order stays the tier's, in code.
// decorate.Decorations turns a component's resolved Layers into its decorate.Deps.
//
// The default observes each request at its edges — its controller and workflow, and
// every client, job and gate it calls — and leaves the pipeline, service, repository
// and store tiers unobserved until switched on; resilience runs where today's
// components run it.
package decoration

import (
	"maps"
	"reflect"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
)

// SectionKey is the config key the decoration section lives under.
const SectionKey = "decoration"

// Layers switches and tunes each decoration layer of one component. A nil switch (or
// a nil Timeout) inherits; Merge lays one Layers over another. A layer that is on
// still needs its collaborator (a tracer for tracing, an authorizer for auth), and
// runs only where the tier orders it.
type Layers struct {
	// Recovery turns a panic into an INTERNAL error.
	Recovery *bool `mapstructure:"recovery"`

	// Tracing opens a span per operation.
	Tracing *bool `mapstructure:"tracing"`

	// Metrics records the tier's operation, error and duration series.
	Metrics *bool `mapstructure:"metrics"`

	// Logging logs each operation.
	Logging *bool `mapstructure:"logging"`

	// Auth authorizes each operation as "<name>.<op>".
	Auth *bool `mapstructure:"auth"`

	// Timeout bounds each operation (each attempt, under retry); zero turns it off.
	Timeout *time.Duration `mapstructure:"timeout"`

	// Retry retries transient failures with exponential backoff.
	Retry Retry `mapstructure:"retry"`

	// CircuitBreaker sheds calls while the component keeps failing.
	CircuitBreaker CircuitBreaker `mapstructure:"circuit_breaker"`

	// RateLimit rejects operations over a token-bucket rate.
	RateLimit RateLimit `mapstructure:"rate_limit"`

	// Bulkhead bounds the operations in flight.
	Bulkhead Bulkhead `mapstructure:"bulkhead"`
}

// Retry switches and tunes the retry layer.
type Retry struct {
	// Enabled turns the layer on.
	Enabled *bool `mapstructure:"enabled"`

	// RetryConfig tunes the backoff.
	resilience.RetryConfig `mapstructure:",squash"`
}

// CircuitBreaker switches and tunes the circuit-breaker layer; the breaker is named
// after the component.
type CircuitBreaker struct {
	// Enabled turns the layer on.
	Enabled *bool `mapstructure:"enabled"`

	// BreakerConfig tunes when the breaker trips and recovers.
	resilience.BreakerConfig `mapstructure:",squash"`
}

// Bulkhead switches and tunes the bulkhead layer.
type Bulkhead struct {
	// Enabled turns the layer on.
	Enabled *bool `mapstructure:"enabled"`

	// Shed rejects an operation when the bulkhead is full (CodeResourceExhausted)
	// instead of waiting for a slot.
	Shed *bool `mapstructure:"shed"`

	// BulkheadConfig tunes the concurrency limit ("channel" or "adaptive").
	resilience.BulkheadConfig `mapstructure:",squash"`
}

// RateLimit switches and tunes the rate-limit layer.
type RateLimit struct {
	// Enabled turns the layer on.
	Enabled *bool `mapstructure:"enabled"`

	// RateLimitConfig tunes the token bucket.
	resilience.RateLimitConfig `mapstructure:",squash"`
}

// On reports whether a layer switch is on (nil is off).
func On(b *bool) bool { return b != nil && *b }

// Merge returns base with every value over sets laid on top: a non-nil switch or
// timeout and a non-zero tuning value win; anything else keeps base.
func Merge(base, over Layers) Layers {
	merged := base
	mergeValue(reflect.ValueOf(&merged).Elem(), reflect.ValueOf(over))
	return merged
}

// mergeValue lays src's set fields over dst, recursing into structs.
func mergeValue(dst, src reflect.Value) {
	for i := range src.NumField() {
		s, d := src.Field(i), dst.Field(i)
		switch s.Kind() {
		case reflect.Struct:
			mergeValue(d, s)
		case reflect.Pointer:
			if !s.IsNil() {
				d.Set(s)
			}
		default:
			if !s.IsZero() {
				d.Set(s)
			}
		}
	}
}

// Tier is one tier's decoration: the Defaults every component gets and the Overrides
// one named component lays over them.
type Tier struct {
	// Overrides maps a component name to the layers it lays over Defaults.
	Overrides map[string]Layers `mapstructure:"overrides"`

	// Defaults are every component's layers.
	Defaults Layers `mapstructure:"defaults"`
}

// For returns the layers of the component named name: Defaults, then the override of
// each dotted prefix of name from the shortest, then name's own override, each laid on
// the last. "api.documents.create" gets Defaults, "api", "api.documents", then its own,
// so one key covers a group and a deeper key one component.
func (t Tier) For(name string) Layers {
	return Merge(t.Defaults, t.Overriding(name))
}

// Overriding returns the layers the overrides lay over Defaults for the component named
// name: each dotted prefix's override from the shortest, then name's own.
func (t Tier) Overriding(name string) Layers {
	var l Layers
	for i := range len(name) {
		if name[i] == '.' {
			l = Merge(l, t.Overrides[name[:i]])
		}
	}
	return Merge(l, t.Overrides[name])
}

// Sources returns the config keys For merges for the component named name, in order:
// "defaults" when the tier has defaults, then "overrides.<key>" for each dotted prefix of
// name and for name itself that has an override.
func (t Tier) Sources(name string) []string {
	var out []string
	if !reflect.ValueOf(t.Defaults).IsZero() {
		out = append(out, "defaults")
	}
	for i := range len(name) {
		if name[i] == '.' {
			if _, ok := t.Overrides[name[:i]]; ok {
				out = append(out, "overrides."+name[:i])
			}
		}
	}
	if _, ok := t.Overrides[name]; ok {
		out = append(out, "overrides."+name)
	}
	return out
}

// overlay lays over on t: its defaults over t's, and each override over t's override
// of the same name.
func (t Tier) overlay(over Tier) Tier {
	out := Tier{Defaults: Merge(t.Defaults, over.Defaults)}
	if t.Overrides == nil && over.Overrides == nil {
		return out
	}
	out.Overrides = make(map[string]Layers, len(t.Overrides)+len(over.Overrides))
	maps.Copy(out.Overrides, t.Overrides)
	for name := range over.Overrides {
		out.Overrides[name] = Merge(out.Overrides[name], over.Overrides[name])
	}
	return out
}

// Config is the decoration of every tier.
type Config struct {
	// Controllers decorates the controller (transport-handler) tier
	// (decoration.controllers).
	Controllers Tier `mapstructure:"controllers"`

	// Clients decorates the clients tier (decoration.clients).
	Clients Tier `mapstructure:"clients"`

	// Workflows decorates the workflow tier (decoration.workflows).
	Workflows Tier `mapstructure:"workflows"`

	// Pipelines decorates the pipeline tier (decoration.pipelines).
	Pipelines Tier `mapstructure:"pipelines"`

	// Services decorates the service tier (decoration.services).
	Services Tier `mapstructure:"services"`

	// Repositories decorates the repository tier (decoration.repositories).
	Repositories Tier `mapstructure:"repositories"`

	// Stores decorates the store tier (decoration.stores).
	Stores Tier `mapstructure:"stores"`

	// Jobs decorates the execution-job tier (decoration.jobs).
	Jobs Tier `mapstructure:"jobs"`

	// Gates decorates the execution-gate tier (decoration.gates).
	Gates Tier `mapstructure:"gates"`
}

// Section is one tier's decoration under its config key.
type Section struct {
	// Key is the tier's config key under decoration ("repositories").
	Key string

	// Name is the tier's name in code ("repository"), as decorate.Tier names it.
	Name string

	// Tier is the tier's decoration.
	Tier Tier
}

// Sections returns every tier's decoration under its config key, in config order.
func (c *Config) Sections() []Section {
	return []Section{
		{Key: "controllers", Name: "controller", Tier: c.Controllers},
		{Key: "clients", Name: "client", Tier: c.Clients},
		{Key: "workflows", Name: "workflow", Tier: c.Workflows},
		{Key: "pipelines", Name: "pipeline", Tier: c.Pipelines},
		{Key: "services", Name: "service", Tier: c.Services},
		{Key: "repositories", Name: "repository", Tier: c.Repositories},
		{Key: "stores", Name: "store", Tier: c.Stores},
		{Key: "jobs", Name: "job", Tier: c.Jobs},
		{Key: "gates", Name: "gate", Tier: c.Gates},
	}
}

// Tier returns the decoration of the tier named name ("controller", "client",
// "workflow", "pipeline", "service", "repository", "store", "job", "gate"), and whether
// there is one.
func (c *Config) Tier(name string) (Tier, bool) {
	sections := c.Sections()
	for i := range sections {
		if sections[i].Name == name {
			return sections[i].Tier, true
		}
	}
	return Tier{}, false
}

// Overlay lays over on c, tier by tier (see Tier.overlay).
func (c *Config) Overlay(over *Config) Config {
	return Config{
		Controllers:  c.Controllers.overlay(over.Controllers),
		Clients:      c.Clients.overlay(over.Clients),
		Workflows:    c.Workflows.overlay(over.Workflows),
		Pipelines:    c.Pipelines.overlay(over.Pipelines),
		Services:     c.Services.overlay(over.Services),
		Repositories: c.Repositories.overlay(over.Repositories),
		Stores:       c.Stores.overlay(over.Stores),
		Jobs:         c.Jobs.overlay(over.Jobs),
		Gates:        c.Gates.overlay(over.Gates),
	}
}

// DefaultConfig returns today's decoration: recovery and auth wherever a tier orders
// them; every controller, client, workflow, job and gate observed, and the pipeline,
// service, repository and store tiers not; retry and a circuit breaker at the
// repository tier; and a 30 s timeout, a bulkhead and a breaker around the storage,
// outbox and Prometheus clients. Every other resilience layer, the controllers' rate
// limit and bulkhead included, is off; a controller's bulkhead, once enabled, sheds.
func DefaultConfig() Config {
	repos := base(false)
	repos.Retry.Enabled = ptr(true)
	repos.CircuitBreaker.Enabled = ptr(true)
	// A controller's bulkhead, once enabled, sheds: a request queued behind a full
	// bulkhead holds its connection, so the edge rejects it instead.
	controllers := base(true)
	controllers.Bulkhead.Shed = ptr(true)
	guarded := func() Layers {
		return Layers{
			Timeout:        ptr(30 * time.Second),
			Bulkhead:       Bulkhead{Enabled: ptr(true)},
			CircuitBreaker: CircuitBreaker{Enabled: ptr(true)},
		}
	}
	return Config{
		Clients: Tier{Defaults: base(true), Overrides: map[string]Layers{
			"storage":    guarded(),
			"outbox.sqs": guarded(),
			"outbox.s3":  guarded(),
			"prometheus": guarded(),
		}},
		Controllers:  Tier{Defaults: controllers},
		Workflows:    Tier{Defaults: base(true)},
		Pipelines:    Tier{Defaults: base(false)},
		Services:     Tier{Defaults: base(false)},
		Repositories: Tier{Defaults: repos},
		Stores:       Tier{Defaults: base(false)},
		Jobs:         Tier{Defaults: base(true)},
		Gates:        Tier{Defaults: base(true)},
	}
}

// base is a tier's default layers: recovery and auth on, observability as observe
// says, every resilience layer off with its default tuning.
func base(observe bool) Layers {
	return Layers{
		Recovery: ptr(true),
		Tracing:  ptr(observe),
		Metrics:  ptr(observe),
		Logging:  ptr(observe),
		Auth:     ptr(true),
		Timeout:  ptr(time.Duration(0)),
		Retry: Retry{
			Enabled:     ptr(false),
			RetryConfig: resilience.DefaultRetryConfig(),
		},
		CircuitBreaker: CircuitBreaker{
			Enabled:       ptr(false),
			BreakerConfig: resilience.DefaultBreakerConfig(),
		},
		Bulkhead: Bulkhead{
			Enabled:        ptr(false),
			Shed:           ptr(false),
			BulkheadConfig: resilience.DefaultBulkheadConfig(),
		},
		RateLimit: RateLimit{
			Enabled:         ptr(false),
			RateLimitConfig: resilience.DefaultRateLimitConfig(),
		},
	}
}

// ptr returns a fresh pointer to v.
func ptr[T any](v T) *T { return &v }

// Load reads the decoration section from loader and lays it over DefaultConfig(): an
// absent section keeps the default, a partial one changes only what it sets, and a
// section that fails to decode is returned as CodeInvalidInput.
func Load(loader interfaces.ConfigLoader) (Config, error) {
	section := loader.Get(SectionKey)
	if section == nil {
		return DefaultConfig(), nil
	}
	var over Config
	if err := loader.UnmarshalKey(SectionKey, &over); err != nil {
		return Config{}, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"load decoration config",
		)
	}
	if err := dottedOverrides(loader, section, &over); err != nil {
		return Config{}, err
	}
	def := DefaultConfig()
	return def.Overlay(&over), nil
}

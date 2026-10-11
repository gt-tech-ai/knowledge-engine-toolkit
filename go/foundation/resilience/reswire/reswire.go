// Package reswire builds resilience primitives (retrier, circuit breaker) from a
// ConfigLoader + the schema/resilience config surface. It is the
// single composition helper the repo/client/interceptor providers use, replacing
// the former zero-option retry.New / circuitbreaker.New that froze the
// decorators at DefaultConfig(). Starting from the schema defaults (== each
// primitive's DefaultConfig) keeps an absent overlay behavior-preserving.
package reswire

import (
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	schemares "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/adaptivethrottle"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/hedge"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
)

// NewRetrier builds an exponential-backoff retrier tuned from the
// resilience.retry config section, overlaid on the behavior-preserving defaults.
// An absent section keeps the defaults (== retry.DefaultConfig()).
func NewRetrier(loader interfaces.ConfigLoader) (interfaces.Retrier, error) {
	c := schemares.DefaultRetryConfig()
	if loader.Get("resilience.retry") != nil {
		if err := loader.UnmarshalKey("resilience.retry", &c); err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load resilience.retry",
			)
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return retry.NewFromConfig(retry.Config{
		Kind:            retry.KindExponential,
		MaxRetries:      c.MaxRetries,
		InitialInterval: c.InitialInterval,
		MaxInterval:     c.MaxInterval,
		Multiplier:      c.Multiplier,
		MaxElapsedTime:  c.MaxElapsedTime,
	})
}

// NewBreaker builds a gobreaker circuit breaker named name, tuned from the
// resilience.circuit_breaker config section overlaid on the behavior-preserving
// defaults. name identifies the call site (api.usercontext, identity.users.repo, …).
func NewBreaker(
	loader interfaces.ConfigLoader,
	name string,
) (interfaces.CircuitBreaker, error) {
	c := schemares.DefaultBreakerConfig()
	if loader.Get("resilience.circuit_breaker") != nil {
		if err := loader.UnmarshalKey("resilience.circuit_breaker", &c); err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load resilience.circuit_breaker",
			)
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return circuitbreaker.NewFromConfig(circuitbreaker.Config{
		Kind:                circuitbreaker.KindGoBreaker,
		Name:                name,
		MaxRequests:         c.MaxRequests,
		Interval:            c.Interval,
		Timeout:             c.Timeout,
		ConsecutiveFailures: c.ConsecutiveFailures,
		FailureRatio:        c.FailureRatio,
		MinRequests:         c.MinRequests,
	})
}

// NewBulkhead builds a concurrency-limiting bulkhead tuned from the
// resilience.adaptive_limit config section, overlaid on the caller's fallback
// (defaultMaxConcurrent — the value the call site used to hardcode, so an absent overlay
// is behavior-preserving). An overlay overrides per-field; the kind string
// ("channel"/"adaptive") is mapped to a bulkhead.Kind and fails loudly on an unknown
// value (ARCHITECTURE.md#swappable-components). This brings the previously-inert
// resilience.adaptive_limit sub-tier into use.
func NewBulkhead(
	loader interfaces.ConfigLoader,
	defaultMaxConcurrent int,
) (interfaces.Bulkhead, error) {
	c := schemares.DefaultBulkheadConfig()
	c.MaxConcurrent = defaultMaxConcurrent
	if loader.Get("resilience.adaptive_limit") != nil {
		if err := loader.UnmarshalKey("resilience.adaptive_limit", &c); err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load resilience.adaptive_limit",
			)
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	kind, err := bulkheadKind(c.Kind)
	if err != nil {
		return nil, err
	}
	return bulkhead.NewFromConfig(bulkhead.Config{
		Kind:              kind,
		MaxConcurrent:     c.MaxConcurrent,
		MinConcurrent:     c.MinConcurrent,
		InitialConcurrent: c.InitialConcurrent,
		RTTThreshold:      c.RTTThreshold,
		BackoffRatio:      c.BackoffRatio,
	})
}

// bulkheadKind maps the resilience.adaptive_limit kind string to a bulkhead.Kind, failing
// loudly on an unknown value. Empty defaults to channel (the primitive default).
func bulkheadKind(s string) (bulkhead.Kind, error) {
	switch s {
	case "", "channel":
		return bulkhead.KindChannel, nil
	case "adaptive":
		return bulkhead.KindAdaptive, nil
	default:
		return 0, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"unknown resilience.adaptive_limit kind: %q (want channel|adaptive)",
				s,
			),
		)
	}
}

// NewHedge builds a tail-latency hedger tuned from the resilience.hedge config section,
// overlaid on the behavior-preserving default (disabled — hedging is opt-in per
// idempotent path). An absent section keeps the default; the kind string
// ("disabled"/"delay") is mapped to a hedge.Kind and fails loudly on an unknown value.
// Brings the previously-inert resilience.hedge sub-tier into use
func NewHedge(loader interfaces.ConfigLoader) (interfaces.Hedger, error) {
	c := schemares.DefaultHedgeConfig()
	if loader.Get("resilience.hedge") != nil {
		if err := loader.UnmarshalKey("resilience.hedge", &c); err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load resilience.hedge",
			)
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	kind, err := hedgeKind(c.Kind)
	if err != nil {
		return nil, err
	}
	return hedge.NewFromConfig(hedge.Config{Kind: kind, Delay: c.Delay})
}

// hedgeKind maps the resilience.hedge kind string to a hedge.Kind, failing loudly on an
// unknown value. Empty defaults to disabled (the safe primitive default).
func hedgeKind(s string) (hedge.Kind, error) {
	switch s {
	case "", "disabled":
		return hedge.KindDisabled, nil
	case "delay":
		return hedge.KindDelay, nil
	default:
		return 0, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown resilience.hedge kind: %q (want disabled|delay)", s),
		)
	}
}

// NewAdaptiveThrottle builds a client-side SRE load-shedding throttler tuned from the
// resilience.adaptive_throttle config section, overlaid on the behavior-preserving
// default (disabled — a no-op pass-through). Compose it OUTERMOST of a retry budget so a
// local shed returns before the retrier runs. An absent section keeps the default; the
// kind string ("disabled"/"enabled") is validated and fails loudly on an unknown value.
// Brings the previously-inert resilience.adaptive_throttle sub-tier into use.
func NewAdaptiveThrottle(
	loader interfaces.ConfigLoader,
) (interfaces.AdaptiveThrottler, error) {
	c := schemares.DefaultAdaptiveThrottleConfig()
	if loader.Get("resilience.adaptive_throttle") != nil {
		if err := loader.UnmarshalKey("resilience.adaptive_throttle", &c); err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load resilience.adaptive_throttle",
			)
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	switch c.Kind {
	case "", "disabled":
		return adaptivethrottle.NewDisabled(), nil
	case "enabled":
		return adaptivethrottle.New(adaptivethrottle.Config{K: c.K, Decay: c.Decay}), nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"unknown resilience.adaptive_throttle kind: %q (want disabled|enabled)",
				c.Kind,
			),
		)
	}
}

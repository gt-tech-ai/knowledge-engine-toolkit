// Package reswire builds the resilience primitives that are not decoration layers —
// the tail-latency hedger and the adaptive throttle — from a ConfigLoader and the
// schema/resilience config surface, starting from the schema defaults so an absent
// overlay is behavior-preserving. The retrier, circuit breaker and bulkhead are
// decoration layers, built per component by decorate.Decorations. WithMetrics makes a
// built primitive report its resilience_* series under tier "standalone".
package reswire

import (
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	schemares "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/adaptivethrottle"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/hedge"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"
)

// Wiring is what the Options set on a primitive reswire builds.
type Wiring struct {
	// Metrics is the provider the primitive's resilience_* series go to; nil records
	// nothing.
	Metrics interfaces.Metrics

	// Name is the primitive's name label.
	Name string
}

// Option sets one part of the Wiring.
type Option = options.Option[Wiring]

// WithMetrics makes the built primitive report its resilience_* series through metrics,
// labelled tier "standalone" and name.
func WithMetrics(metrics interfaces.Metrics, name string) Option {
	return func(w *Wiring) {
		w.Metrics, w.Name = metrics, name
	}
}

// recorder returns the series recorder opts wire, nil when they set no metrics.
func recorder(opts []Option) *series.Recorder {
	var wiring Wiring

	options.ApplyOptions(&wiring, opts...)

	return series.NewRecorder(wiring.Metrics, series.TierStandalone, wiring.Name)
}

// NewHedge builds a tail-latency hedger tuned from the resilience.hedge config section,
// overlaid on the behavior-preserving default (disabled — hedging is opt-in per
// idempotent path). An absent section keeps the default; the kind string
// ("disabled"/"delay") is mapped to a hedge.Kind and fails loudly on an unknown value.
// Brings the previously-inert resilience.hedge sub-tier into use. With WithMetrics the
// delay hedger counts its fired and winning backups.
func NewHedge(loader interfaces.ConfigLoader, opts ...Option) (interfaces.Hedger, error) {
	hedgeConfig := schemares.DefaultHedgeConfig()
	if loader.Get("resilience.hedge") != nil {
		err := loader.UnmarshalKey("resilience.hedge", &hedgeConfig)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load resilience.hedge",
			)
		}
	}

	err := hedgeConfig.Validate()
	if err != nil {
		return nil, err
	}

	kind, err := hedgeKind(hedgeConfig.Kind)
	if err != nil {
		return nil, err
	}

	return hedge.NewFromConfig(hedge.Config{
		Recorder: recorder(opts),
		Kind:     kind,
		Delay:    hedgeConfig.Delay,
	})
}

// hedgeKind maps the resilience.hedge kind string to a hedge.Kind, failing loudly on an
// unknown value. Empty defaults to disabled (the safe primitive default).
func hedgeKind(text string) (hedge.Kind, error) {
	switch text {
	case "", "disabled":
		return hedge.KindDisabled, nil
	case "delay":
		return hedge.KindDelay, nil
	default:
		return 0, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown resilience.hedge kind: %q (want disabled|delay)", text),
		)
	}
}

// NewAdaptiveThrottle builds a client-side SRE load-shedding throttler tuned from the
// resilience.adaptive_throttle config section, overlaid on the behavior-preserving
// default (disabled — a no-op pass-through). Compose it OUTERMOST of a retry budget so a
// local shed returns before the retrier runs. An absent section keeps the default; the
// kind string ("disabled"/"enabled") is validated and fails loudly on an unknown value.
// Brings the previously-inert resilience.adaptive_throttle sub-tier into use. With
// WithMetrics the enabled throttler reports its ratio and its shed calls; the disabled
// one never sheds and records nothing.
func NewAdaptiveThrottle(
	loader interfaces.ConfigLoader,
	opts ...Option,
) (interfaces.AdaptiveThrottler, error) {
	adaptiveThrottleConfig := schemares.DefaultAdaptiveThrottleConfig()
	if loader.Get("resilience.adaptive_throttle") != nil {
		err := loader.UnmarshalKey(
			"resilience.adaptive_throttle",
			&adaptiveThrottleConfig,
		)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load resilience.adaptive_throttle",
			)
		}
	}

	err := adaptiveThrottleConfig.Validate()
	if err != nil {
		return nil, err
	}

	switch adaptiveThrottleConfig.Kind {
	case "", "disabled":
		return adaptivethrottle.NewDisabled(), nil
	case "enabled":
		return adaptivethrottle.New(
			adaptivethrottle.Config{
				Recorder: recorder(opts),
				K:        adaptiveThrottleConfig.K,
				Decay:    adaptiveThrottleConfig.Decay,
				Rand:     nil,
			},
		), nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"unknown resilience.adaptive_throttle kind: %q (want disabled|enabled)",
				adaptiveThrottleConfig.Kind,
			),
		)
	}
}

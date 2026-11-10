// Package reswire builds the resilience primitives that are not decoration layers —
// the tail-latency hedger and the adaptive throttle — from a ConfigLoader and the
// schema/resilience config surface, starting from the schema defaults so an absent
// overlay is behavior-preserving. The retrier, circuit breaker and bulkhead are
// decoration layers, built per component by decorate.Decorations.
package reswire

import (
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	schemares "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/adaptivethrottle"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/hedge"
)

// NewHedge builds a tail-latency hedger tuned from the resilience.hedge config section,
// overlaid on the behavior-preserving default (disabled — hedging is opt-in per
// idempotent path). An absent section keeps the default; the kind string
// ("disabled"/"delay") is mapped to a hedge.Kind and fails loudly on an unknown value.
// Brings the previously-inert resilience.hedge sub-tier into use.
func NewHedge(loader interfaces.ConfigLoader) (interfaces.Hedger, error) {
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

	return hedge.NewFromConfig(hedge.Config{Kind: kind, Delay: hedgeConfig.Delay})
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
// Brings the previously-inert resilience.adaptive_throttle sub-tier into use.
func NewAdaptiveThrottle(
	loader interfaces.ConfigLoader,
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
				K:     adaptiveThrottleConfig.K,
				Decay: adaptiveThrottleConfig.Decay,
				Rand:  nil,
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

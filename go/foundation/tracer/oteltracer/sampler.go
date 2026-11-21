package oteltracer

import (
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

const (
	// SamplerRatio samples a root span by the configured rate and honours its parent's
	// decision. It is the default.
	SamplerRatio = "ratio"

	// SamplerLinkAware is SamplerRatio that also samples a root span holding a link to a
	// sampled span, so a batch span is never dropped while a trace it links to is kept.
	SamplerLinkAware = "link_aware"
)

// NewSampler builds the sampler kind selects for rate (0.0 to 1.0; 1.0 = always). An
// empty kind means SamplerRatio; an unknown kind is CodeInvalidInput, so a misspelt
// kind fails the boot instead of sampling by another rule.
//
// Links are read at span start, so a caller that wants SamplerLinkAware to see them
// passes them when it starts the span, never adds them afterwards.
func NewSampler(kind string, rate float64) (sdktrace.Sampler, error) {
	switch kind {
	case "", SamplerRatio:
		return ratioSampler(rate), nil
	case SamplerLinkAware:
		return sdktrace.ParentBased(
			linkAware{ratio: sdktrace.TraceIDRatioBased(rate)},
		), nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"unknown tracer sampler kind "+kind+`: want "`+SamplerRatio+`" or "`+
				SamplerLinkAware+`"`,
		)
	}
}

// ratioSampler picks the sampler for the configured rate.
func ratioSampler(rate float64) sdktrace.Sampler {
	switch {
	case rate >= alwaysSampleRate:
		return sdktrace.AlwaysSample()
	case rate <= 0:
		return sdktrace.NeverSample()
	default:
		// ParentBased so an incoming sampled parent (a W3C traceparent with sampled=1,
		// extracted by the TraceContext propagator) forces the span sampled — the lever
		// an end-to-end telemetry check relies on to guarantee its one request's trace
		// reaches the backend without raising the ratio for normal traffic. A ROOT span
		// with no parent still follows the ratio.
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(rate))
	}
}

// linkAware is the root sampler of SamplerLinkAware: it samples a span holding a link to
// a sampled span and otherwise decides by ratio. Wrapped in ParentBased, it sees only
// root spans, so a child still follows its parent.
type linkAware struct {
	// ratio decides every span with no sampled link.
	ratio sdktrace.Sampler
}

// ShouldSample samples the span when any of its links points at a sampled span context,
// keeping the parent's tracestate, and delegates to the ratio sampler otherwise.
func (sampler linkAware) ShouldSample(
	params sdktrace.SamplingParameters,
) sdktrace.SamplingResult {
	for i := range params.Links {
		if params.Links[i].SpanContext.IsSampled() {
			return sdktrace.SamplingResult{
				Decision:   sdktrace.RecordAndSample,
				Attributes: nil,
				Tracestate: trace.SpanContextFromContext(params.ParentContext).
					TraceState(),
			}
		}
	}

	return sampler.ratio.ShouldSample(params)
}

// Description names the sampler and the ratio sampler it falls back to.
func (sampler linkAware) Description() string {
	return "LinkAware{" + sampler.ratio.Description() + "}"
}

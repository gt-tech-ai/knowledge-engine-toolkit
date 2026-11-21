package unit_test

import (
	"context"
	"testing"
	"testing/quick"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer/oteltracer"
)

const (
	// linkSamplerTraceID is the trace id every sampler test span carries.
	linkSamplerTraceID = "0123456789abcdef0123456789abcdef"

	// linkSamplerSpanID is the span id of every span context the sampler tests build.
	linkSamplerSpanID = "0123456789abcdef"

	// linkSamplerNever is a rate at which the ratio sampler drops every root.
	linkSamplerNever = 0.0

	// linkSamplerAlways is a rate at which the ratio sampler keeps every root.
	linkSamplerAlways = 1.0
)

// linkSamplerContext returns a span context with the given sampled flag.
func linkSamplerContext(
	t *testing.T,
	flags oteltrace.TraceFlags,
	remote bool,
) oteltrace.SpanContext {
	t.Helper()

	tid, err := oteltrace.TraceIDFromHex(linkSamplerTraceID)
	require.NoError(t, err)

	sid, err := oteltrace.SpanIDFromHex(linkSamplerSpanID)
	require.NoError(t, err)

	return oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: flags,
		Remote:     remote,
		TraceState: oteltrace.TraceState{},
	})
}

// samplingParams returns the parameters of a span started in parent with links.
func samplingParams(
	parent context.Context,
	t *testing.T,
	links ...oteltrace.Link,
) sdktrace.SamplingParameters {
	t.Helper()

	tid, err := oteltrace.TraceIDFromHex(linkSamplerTraceID)
	require.NoError(t, err)

	return sdktrace.SamplingParameters{
		ParentContext: parent,
		TraceID:       tid,
		Name:          "span",
		Kind:          oteltrace.SpanKindInternal,
		Attributes:    nil,
		Links:         links,
	}
}

// linkTo returns a link to a span context with the given sampled flag.
func linkTo(t *testing.T, flags oteltrace.TraceFlags) oteltrace.Link {
	t.Helper()

	return oteltrace.Link{
		SpanContext: linkSamplerContext(t, flags, true),
		Attributes:  nil,
	}
}

// newLinkAware builds the link_aware sampler at rate.
func newLinkAware(t *testing.T, rate float64) sdktrace.Sampler {
	t.Helper()

	sampler, err := oteltracer.NewSampler(oteltracer.SamplerLinkAware, rate)
	require.NoError(t, err)

	return sampler
}

// TestLinkAwareSampler_SamplesARootLinkedToASampledSpan tests the sampler's one
// addition to the ratio sampler.
//
// Why this test is important:
//   - A batch span (one KB round) links to every document it carries and is a root of
//     its own trace. At a low ratio the round would be dropped while a sampled
//     document's trace points at it, leaving a dangling link; a sampled linked span must
//     keep the round.
//
// What it tests:
//   - At a rate that drops every root, a root span with a link to a sampled span
//     context is sampled.
//   - A link to an unsampled span context does not force sampling.
func TestLinkAwareSampler_SamplesARootLinkedToASampledSpan(t *testing.T) {
	t.Parallel()

	sampler := newLinkAware(t, linkSamplerNever)
	root := context.Background()

	linked := sampler.ShouldSample(samplingParams(
		root, t, linkTo(t, oteltrace.FlagsSampled), linkTo(t, 0),
	))
	unsampledOnly := sampler.ShouldSample(samplingParams(root, t, linkTo(t, 0)))

	assert.Equal(t, sdktrace.RecordAndSample, linked.Decision)
	assert.Equal(t, sdktrace.Drop, unsampledOnly.Decision)
}

// TestLinkAwareSampler_UnlinkedRootFollowsTheRatio tests that the sampler keeps the
// ratio sampler's behaviour for a root with no links.
//
// Why this test is important:
//   - Link awareness must not raise trace volume for ordinary traffic; a root without a
//     sampled link is sampled exactly as the ratio sampler decides.
//
// What it tests:
//   - An unlinked root is dropped at rate 0 and sampled at rate 1.
func TestLinkAwareSampler_UnlinkedRootFollowsTheRatio(t *testing.T) {
	t.Parallel()

	root := context.Background()

	dropped := newLinkAware(t, linkSamplerNever).ShouldSample(samplingParams(root, t))
	kept := newLinkAware(t, linkSamplerAlways).ShouldSample(samplingParams(root, t))

	assert.Equal(t, sdktrace.Drop, dropped.Decision)
	assert.Equal(t, sdktrace.RecordAndSample, kept.Decision)
}

// TestLinkAwareSampler_HonoursTheParent tests that a span with a parent follows the
// parent, as the ratio sampler does.
//
// Why this test is important:
//   - A request that arrives with a sampled traceparent is force-sampled by callers who
//     rely on it, and a child of an unsampled parent must stay out of the trace; the
//     link rule applies to roots only, so those guarantees hold unchanged.
//
// What it tests:
//   - At rate 0, a child of a sampled remote parent is sampled.
//   - At rate 1, a child of an unsampled remote parent is dropped, even with a link to a
//     sampled span.
func TestLinkAwareSampler_HonoursTheParent(t *testing.T) {
	t.Parallel()

	sampledParent := oteltrace.ContextWithRemoteSpanContext(
		context.Background(),
		linkSamplerContext(t, oteltrace.FlagsSampled, true),
	)
	unsampledParent := oteltrace.ContextWithRemoteSpanContext(
		context.Background(),
		linkSamplerContext(t, 0, true),
	)

	kept := newLinkAware(t, linkSamplerNever).ShouldSample(
		samplingParams(sampledParent, t),
	)
	dropped := newLinkAware(t, linkSamplerAlways).ShouldSample(
		samplingParams(unsampledParent, t, linkTo(t, oteltrace.FlagsSampled)),
	)

	assert.Equal(t, sdktrace.RecordAndSample, kept.Decision)
	assert.Equal(t, sdktrace.Drop, dropped.Decision)
}

// TestNewSampler_Kinds tests the sampler kind selection.
//
// Why this test is important:
//   - The kind is config. An empty kind must keep the existing ratio behaviour so a
//     service that sets nothing is unchanged, and a misspelt kind must fail the boot
//     loudly instead of silently sampling by another rule.
//
// What it tests:
//   - The empty kind and "ratio" build a sampler that does not look at links.
//   - An unknown kind is CodeInvalidInput and builds no sampler.
//   - The link_aware sampler names itself in its description.
func TestNewSampler_Kinds(t *testing.T) {
	t.Parallel()

	root := context.Background()
	linked := samplingParams(root, t, linkTo(t, oteltrace.FlagsSampled))

	for _, kind := range []string{"", oteltracer.SamplerRatio} {
		sampler, err := oteltracer.NewSampler(kind, linkSamplerNever)
		require.NoError(t, err, "kind %q", kind)
		assert.Equal(
			t, sdktrace.Drop, sampler.ShouldSample(linked).Decision, "kind %q", kind,
		)
	}

	sampler, err := oteltracer.NewSampler("link-aware", linkSamplerNever)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Nil(t, sampler)

	assert.Contains(
		t, newLinkAware(t, linkSamplerNever).Description(), "LinkAware",
	)
}

// TestLinkAwareSampler_IsRatioPlusSampledLinks tests the sampler against its
// specification over generated inputs.
//
// Why this test is important:
//   - The sampler's contract is exact: it samples every root the ratio sampler samples
//     and every root with a sampled link, and nothing else. Example tests pin the
//     corners; this pins the whole input space, so a change that raises trace volume or
//     drops a linked batch span for some trace id or rate is caught.
//
// What it tests:
//   - For random trace ids, rates in [0, 1] and link sets with random sampled flags, a
//     root span is sampled by link_aware exactly when the ratio sampler samples it or
//     any link is sampled.
func TestLinkAwareSampler_IsRatioPlusSampledLinks(t *testing.T) {
	t.Parallel()

	const rateSteps = 1000

	property := func(traceID oteltrace.TraceID, rateStep uint16, flags []bool) bool {
		rate := float64(rateStep%(rateSteps+1)) / rateSteps

		links := make([]oteltrace.Link, 0, len(flags))
		anySampled := false

		for _, sampled := range flags {
			traceFlags := oteltrace.TraceFlags(0)
			if sampled {
				traceFlags = oteltrace.FlagsSampled
				anySampled = true
			}

			links = append(links, linkTo(t, traceFlags))
		}

		params := samplingParams(context.Background(), t, links...)

		params.TraceID = traceID

		ratio, err := oteltracer.NewSampler(oteltracer.SamplerRatio, rate)
		require.NoError(t, err)

		want := anySampled ||
			ratio.ShouldSample(params).Decision == sdktrace.RecordAndSample
		got := newLinkAware(t, rate).ShouldSample(params).Decision ==
			sdktrace.RecordAndSample

		return got == want
	}

	require.NoError(t, quick.Check(property, nil))
}

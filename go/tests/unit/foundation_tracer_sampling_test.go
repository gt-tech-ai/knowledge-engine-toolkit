package unit_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
)

// remoteSampledParent returns a context carrying a REMOTE parent span context with the
// sampled trace-flag set — the shape the W3C `traceparent` header produces after
// propagation.TraceContext extracts it on an incoming request (the path a client's
// force-sampled header triggers).
func remoteSampledParent(t *testing.T) context.Context {
	t.Helper()
	tid, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	sid, err := oteltrace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
	return oteltrace.ContextWithRemoteSpanContext(context.Background(), sc)
}

// TestTracer_HonorsSampledParent tests that the OTel tracer records a child span when its
// remote parent is sampled, even at an effectively-zero sample rate.
//
// Why this test is important:
//   - A caller (e.g. an end-to-end telemetry check) sends a `traceparent` with the
//     sampled flag so its one request is force-sampled and its trace reaches the backend,
//     while normal traffic keeps the configured ratio. That only works if the sampler is
//     ParentBased (honors the parent's decision); with a bare TraceIDRatioBased sampler
//     the parent flag is ignored and the trace is dropped at the configured ratio.
//
// What it tests:
//   - Given a fractional sample rate so small the ratio would reject every root, a span
//     started under a sampled remote parent is itself sampled (observed via the standard
//     context Start returns).
func TestTracer_HonorsSampledParent(t *testing.T) {
	ctx := context.Background()
	tr, err := tracer.New(
		ctx,
		tracer.KindOTel,
		tracer.WithServiceName("tracer-sampling-test"),
		tracer.WithEndpoint("localhost:4317"),
		tracer.WithSampleRate(
			1e-9,
		), // fractional (not 0) → TraceIDRatioBased branch, rejects ~every root
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.Shutdown(context.Background()) })

	// The sampled span is deliberately never ended: ending it would queue it for export
	// to the collector at Shutdown (a network dial and a 30s retry in a unit test).
	retCtx, _ := tr.Start(remoteSampledParent(t), "child")

	assert.True(
		t,
		oteltrace.SpanFromContext(retCtx).SpanContext().IsSampled(),
		"a sampled remote parent must force the child span sampled (ParentBased), even at "+
			"~0 ratio",
	)
}

// TestTracer_RootSpanKeepsRatio tests that a ROOT span (no incoming parent) still follows
// the configured sample ratio — the ParentBased wrap must not change normal production
// sampling.
//
// Why this test is important:
//   - The ParentBased wrap only forces sampling for a request that arrives WITH a sampled
//     parent. Production traffic (a root span, no parent) must keep sampling at the
//     configured ratio, not jump to always-on, or trace volume and cost regress. This
//     guards that invariant.
//
// What it tests:
//   - Given an effectively-zero sample rate and NO parent, a root span is not sampled
//     (the ratio still governs roots after the ParentBased wrap).
func TestTracer_RootSpanKeepsRatio(t *testing.T) {
	ctx := context.Background()
	tr, err := tracer.New(
		ctx,
		tracer.KindOTel,
		tracer.WithServiceName("tracer-sampling-root-test"),
		tracer.WithEndpoint("localhost:4317"),
		tracer.WithSampleRate(1e-9),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.Shutdown(context.Background()) })

	retCtx, span := tr.Start(context.Background(), "root")
	defer span.End()

	assert.False(
		t,
		oteltrace.SpanFromContext(retCtx).SpanContext().IsSampled(),
		"a root span with no parent must follow the (near-zero) ratio, not be force-sampled",
	)
}

// TestContextWithForcedSample tests that a span started from ContextWithForcedSample is
// sampled even at an effectively-zero ratio, and carries a trace id.
//
// Why this test is important:
//   - This is the server-side force-sample lever for a root span that has no client
//     traceparent to honour (e.g. a WebSocket message handler). Without it, such a trace
//     is dropped at the configured ratio. It must produce a sampled, non-empty trace the
//     client can then correlate in the tracing backend.
//
// What it tests:
//   - A span started from the forced context is sampled (ParentBased honours the seeded
//     sampled remote parent) and TraceIDFromContext yields its 32-hex id.
func TestContextWithForcedSample(t *testing.T) {
	ctx := context.Background()
	tr, err := tracer.New(
		ctx,
		tracer.KindOTel,
		tracer.WithServiceName("tracer-forced-sample-test"),
		tracer.WithEndpoint("localhost:4317"),
		tracer.WithSampleRate(1e-9),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tr.Shutdown(context.Background()) })

	retCtx, span := tr.Start(
		tracer.ContextWithForcedSample(context.Background()),
		"ws.query",
	)
	defer span.End()

	assert.True(
		t,
		oteltrace.SpanFromContext(retCtx).SpanContext().IsSampled(),
		"a span started from the forced context must be sampled even at a near-zero ratio",
	)
	assert.Len(
		t,
		tracer.TraceIDFromContext(retCtx),
		32,
		"the forced span carries a 32-hex trace id",
	)
}

// TestTracer_ConcurrentNewKeepsOwnSampler tests that tracers built concurrently with
// different sample rates each keep their own sampler.
//
// Why this test is important:
//   - Every New installs its provider as the OTel global. A tracer taken from the global
//     instead of from its own provider picks up whichever provider a concurrent New
//     installed last, so a rate-0 tracer can start recording (and exporting) spans, or a
//     rate-1 tracer can drop them.
//
// What it tests:
//   - With rate-0 and rate-1 tracers created in parallel, every rate-0 root span is
//     non-recording and every rate-1 root span is recording.
func TestTracer_ConcurrentNewKeepsOwnSampler(t *testing.T) {
	const pairs = 64
	ctx := t.Context()

	type result struct {
		err       error
		rate      float64
		recording bool
	}
	results := make(chan result, 2*pairs)
	var wg sync.WaitGroup
	for i := range 2 * pairs {
		rate := float64(i % 2)
		wg.Go(func() {
			tr, err := tracer.New(ctx, tracer.KindOTel,
				tracer.WithServiceName("tracer-concurrent-test"),
				tracer.WithEndpoint("localhost:4317"),
				tracer.WithSampleRate(rate),
			)
			if err != nil {
				results <- result{err: err}
				return
			}
			t.Cleanup(func() { _ = tr.Shutdown(context.WithoutCancel(ctx)) })
			// Spans are never ended, so nothing is queued for export to the collector.
			retCtx, _ := tr.Start(ctx, "root")
			results <- result{
				rate: rate, recording: oteltrace.SpanFromContext(retCtx).IsRecording(),
			}
		})
	}
	wg.Wait()
	close(results)

	for r := range results {
		require.NoError(t, r.err)
		assert.Equal(
			t,
			r.rate == 1,
			r.recording,
			"a tracer at rate %v must follow its own sampler",
			r.rate,
		)
	}
}

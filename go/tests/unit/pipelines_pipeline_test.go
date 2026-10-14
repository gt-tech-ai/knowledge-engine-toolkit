package unit_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestPipelineTier_Order tests the pipeline tier's decorator order.
//
// Why this test is important:
//   - The order is the tier's documented contract (ARCHITECTURE.md#decorator-order):
//     recovery must be outermost so a panic anywhere in a stage becomes an error.
//
// What it tests:
//   - The tier is named "pipeline" and orders recovery → logging → tracing → metrics →
//     timeout.
func TestPipelineTier_Order(t *testing.T) {
	t.Parallel()
	assert.Equal(t, decorate.Tier{Name: "pipeline", Order: []decorate.Layer{
		decorate.LayerRecovery, decorate.LayerLogging, decorate.LayerTracing,
		decorate.LayerMetrics, decorate.LayerTimeout,
	}}, pipeline.Tier())
}

// TestPipelineStage_ThenComposesAndStopsOnError tests stage composition.
//
// Why this test is important:
//   - Then is how a staged transformation (compile → read → reduce) becomes one
//     pipeline; a stage that saw the wrong input corrupts every result downstream, and a
//     failed stage that did not stop the chain would run work nobody asked for.
//
// What it tests:
//   - Each stage receives the previous stage's output and chains nest.
//   - An error from a stage is returned unchanged and the next stage never runs.
//   - A nil next stage or a nil stage function panics at wiring time.
func TestPipelineStage_ThenComposesAndStopsOnError(t *testing.T) {
	t.Parallel()
	stages := pipeline.Base{}
	parse := stages.Stage("parse", func(_ context.Context, s string) (int, error) {
		return strconv.Atoi(s)
	})
	double := stages.Stage("double", func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	})
	format := stages.Stage("format", func(_ context.Context, n int) (string, error) {
		return "n=" + strconv.Itoa(n), nil
	})

	got, err := parse.Then(double.Then(format)).Execute(t.Context(), "21")
	require.NoError(t, err)
	assert.Equal(t, "n=42", got)

	boom := apperr.New(apperr.CodeInvalidInput, "bad spec")
	failing := stages.Stage("fail", func(context.Context, string) (int, error) {
		return 0, boom
	})
	untouched := mocks.NewMockPipeline[int, string](gomock.NewController(t))
	_, err = failing.Then(untouched).Execute(t.Context(), "x")
	require.ErrorIs(t, err, boom)

	assert.Panics(t, func() { failing.Then(interfaces.Pipeline[int, string](nil)) })
	assert.Panics(t, func() {
		stages.Stage[int, int]("nil", nil)
	})
}

// TestPipelineStage_RecoversAndObservesEachStage tests a stage's decoration.
//
// Why this test is important:
//   - A panicking stage must fail its request with a coded error, not crash the server,
//     and a staged pipeline is only diagnosable if each stage is its own span.
//
// What it tests:
//   - A panicking stage returns an INTERNAL error, even from the zero Base.
//   - With the tier observed, two composed stages start one span each, named
//     "<stage>.Execute", in order.
func TestPipelineStage_RecoversAndObservesEachStage(t *testing.T) {
	t.Parallel()
	_, err := pipeline.Base{}.Stage("boom", func(context.Context, int) (int, error) {
		panic("boom")
	}).Execute(t.Context(), 1)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))

	ctrl := gomock.NewController(t)
	tracer := mocks.NewMockTracer(ctrl)
	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().End().Times(2)
	start := func(
		ctx context.Context, _ string, _ ...interfaces.SpanOption,
	) (context.Context, interfaces.Span) {
		return ctx, span
	}
	gomock.InOrder(
		tracer.EXPECT().
			Start(gomock.Any(), "compile.Execute", gomock.Any()).
			DoAndReturn(start),
		tracer.EXPECT().
			Start(gomock.Any(), "reduce.Execute", gomock.Any()).
			DoAndReturn(start),
	)
	stages := pipeline.NewBase(decorate.Deps{
		Observability: decorate.Observability{Tracer: tracer},
	})
	compile := stages.Stage("compile", func(_ context.Context, s string) (int, error) {
		return len(s), nil
	})
	reduce := stages.Stage("reduce", func(_ context.Context, n int) (bool, error) {
		return n > 2, nil
	})

	got, err := compile.Then(reduce).Execute(t.Context(), "spec")
	require.NoError(t, err)
	assert.True(t, got)
}

// TestPipelineFanOut_RunsEveryStageOnTheInputAndKeepsOrder tests the fan-out stage.
//
// Why this test is important:
//   - A fan-out gathers independent reads (one per source) for one request; a result in
//     the wrong slot would attribute one source's answer to another, and a failure that
//     did not stop its siblings would keep spending on a request that already failed.
//
// What it tests:
//   - Every stage runs on the same input concurrently (each waits for all to start) and
//     the results come back in stage order.
//   - A failing stage's error is returned and cancels the context its siblings run on.
//   - A fan-out with no stages returns an empty result.
func TestPipelineFanOut_RunsEveryStageOnTheInputAndKeepsOrder(t *testing.T) {
	t.Parallel()
	stages := pipeline.Base{}
	started := make(chan struct{}, 3)
	all := make(chan struct{})
	go func() {
		for range 3 {
			<-started
		}
		close(all)
	}()
	times := func(k int) interfaces.Pipeline[int, int] {
		return pipeline.Stage[int, int](func(ctx context.Context, n int) (int, error) {
			started <- struct{}{}
			select {
			case <-all:
				return n * k, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		})
	}

	got, err := stages.FanOut("multiply", times(1), times(2), times(3)).
		Execute(t.Context(), 7)
	require.NoError(t, err)
	assert.Equal(t, []int{7, 14, 21}, got)

	boom := apperr.New(apperr.CodeUnavailable, "source down")
	failing := pipeline.Stage[int, int](func(context.Context, int) (int, error) {
		return 0, boom
	})
	blocked := pipeline.Stage[int, int](func(ctx context.Context, _ int) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	_, err = stages.FanOut("sources", blocked, failing).Execute(t.Context(), 1)
	require.ErrorIs(t, err, boom)

	none, err := stages.FanOut[int, int]("none").Execute(t.Context(), 1)
	require.NoError(t, err)
	assert.Empty(t, none)
}

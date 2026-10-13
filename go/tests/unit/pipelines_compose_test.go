package unit_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestThen_FeedsFirstOutputToSecond tests pipeline composition.
//
// Why this test is important:
//   - Then is how a staged transformation (compile → read → reduce) becomes one pipeline;
//     a stage that saw the wrong input would corrupt every result downstream.
//
// What it tests:
//   - The second stage receives the first stage's output, the composition returns the
//     second's output, and composition nests (Then of Then).
func TestThen_FeedsFirstOutputToSecond(t *testing.T) {
	t.Parallel()
	parse := pipeline.NewBasePipeline(func(_ context.Context, s string) (int, error) {
		return strconv.Atoi(s)
	})
	double := pipeline.NewBasePipeline(func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	})
	format := pipeline.NewBasePipeline(func(_ context.Context, n int) (string, error) {
		return "n=" + strconv.Itoa(n), nil
	})

	got, err := parse.Then(double.Then(format)).Execute(t.Context(), "21")
	require.NoError(t, err)
	require.Equal(t, "n=42", got)
}

// TestThen_FirstErrorSkipsSecond tests composition's failure path.
//
// Why this test is important:
//   - A failed stage must stop the chain: reading a store with a plan that failed to
//     compile would run a query nobody asked for.
//
// What it tests:
//   - An error from the first stage is returned unchanged and the second stage never
//     runs; a nil next stage panics at composition time.
func TestThen_FirstErrorSkipsSecond(t *testing.T) {
	t.Parallel()
	boom := apperr.New(apperr.CodeInvalidInput, "bad spec")
	first := pipeline.NewBasePipeline(func(context.Context, string) (int, error) {
		return 0, boom
	})
	second := mocks.NewMockPipeline[int, string](gomock.NewController(t))

	_, err := first.Then(second).Execute(t.Context(), "x")
	require.ErrorIs(t, err, boom)

	require.Panics(t, func() { first.Then(interfaces.Pipeline[int, string](nil)) })
}

// TestThen_DecoratedStagesEachRecordTheirOwnSpan tests composition with decorators.
//
// Why this test is important:
//   - A staged pipeline is only observable if each stage keeps its own decorators; one
//     span over the whole chain would hide which stage was slow or failed.
//
// What it tests:
//   - Composing two stages built by an observed pipeline.Base starts one span per
//     stage, named after the stage, in order.
func TestThen_DecoratedStagesEachRecordTheirOwnSpan(t *testing.T) {
	t.Parallel()
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
		tracer.EXPECT().Start(gomock.Any(), "compile.Execute").DoAndReturn(start),
		tracer.EXPECT().Start(gomock.Any(), "reduce.Execute").DoAndReturn(start),
	)

	stages := pipeline.NewBase(decorate.Observability{Tracer: tracer})
	compile := stages.Stage("compile", func(_ context.Context, s string) (int, error) {
		return len(s), nil
	})
	reduce := stages.Stage("reduce", func(_ context.Context, n int) (bool, error) {
		return n > 2, nil
	})

	got, err := compile.Then(reduce).Execute(t.Context(), "spec")
	require.NoError(t, err)
	require.True(t, got)
}

// TestFanOut_RunsEveryStageOnTheInputAndKeepsOrder tests the concurrent fan-out stage.
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
func TestFanOut_RunsEveryStageOnTheInputAndKeepsOrder(t *testing.T) {
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
		return pipeline.NewBasePipeline(func(ctx context.Context, n int) (int, error) {
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
	require.Equal(t, []int{7, 14, 21}, got)

	boom := apperr.New(apperr.CodeUnavailable, "source down")
	failing := pipeline.NewBasePipeline(func(context.Context, int) (int, error) {
		return 0, boom
	})
	blocked := pipeline.NewBasePipeline(func(ctx context.Context, _ int) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	_, err = stages.FanOut("sources", blocked, failing).Execute(t.Context(), 1)
	require.ErrorIs(t, err, boom)

	none, err := stages.FanOut[int, int]("none").Execute(t.Context(), 1)
	require.NoError(t, err)
	require.Empty(t, none)
}

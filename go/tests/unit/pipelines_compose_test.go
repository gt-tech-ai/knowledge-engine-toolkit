package unit_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline/decorators"
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

	got, err := pipeline.Then(parse, pipeline.Then(double, format)).
		Execute(t.Context(), "21")
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
//     runs; a nil stage panics at composition time.
func TestThen_FirstErrorSkipsSecond(t *testing.T) {
	t.Parallel()
	boom := apperr.New(apperr.CodeInvalidInput, "bad spec")
	first := pipeline.NewBasePipeline(func(context.Context, string) (int, error) {
		return 0, boom
	})
	second := mocks.NewMockPipeline[int, string](gomock.NewController(t))

	_, err := pipeline.Then(first, second).Execute(t.Context(), "x")
	require.ErrorIs(t, err, boom)

	require.Panics(t, func() { pipeline.Then[string, int, string](nil, second) })
	require.Panics(t, func() { pipeline.Then[string, int, string](first, nil) })
}

// TestThen_DecoratedStagesEachRecordTheirOwnSpan tests composition with decorators.
//
// Why this test is important:
//   - A staged pipeline is only observable if each stage keeps its own decorators; one
//     span over the whole chain would hide which stage was slow or failed.
//
// What it tests:
//   - Composing two stages built with decorators.NewBuilder(...).WithTracing starts one
//     span per stage, named after the stage, in order.
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

	compile := decorators.NewBuilder(
		pipeline.NewBasePipeline(func(_ context.Context, s string) (int, error) {
			return len(s), nil
		}), "compile").WithTracing(tracer).Build()
	reduce := decorators.NewBuilder(
		pipeline.NewBasePipeline(func(_ context.Context, n int) (bool, error) {
			return n > 2, nil
		}), "reduce").WithTracing(tracer).Build()

	got, err := pipeline.Then(compile, reduce).Execute(t.Context(), "spec")
	require.NoError(t, err)
	require.True(t, got)
}

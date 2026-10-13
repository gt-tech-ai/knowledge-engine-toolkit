package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestPipelineBase_StagesRecoverAndComposeWithThen tests the pipeline base's stage
// builder.
//
// Why this test is important:
//   - Every request pipeline is a chain of stages; a panicking stage must fail its
//     request with a coded error, not crash the server, and an unobserved pipeline tier
//     must not touch the observability collaborators.
//
// What it tests:
//   - Stages built from one Base compose with Then and return the composed result.
//   - A panicking stage fails with an INTERNAL error.
//   - With the pipeline tier unobserved (a zero Base) no logger, metrics or tracer is
//     used; with it observed, a stage's span is started (the strict tracer mock records
//     the call).
func TestPipelineBase_StagesRecoverAndComposeWithThen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	quiet := pipeline.Base{}
	double := quiet.Stage("double", func(_ context.Context, n int) (int, error) {
		return n * 2, nil
	})
	label := quiet.Stage("label", func(_ context.Context, n int) (string, error) {
		return string(rune('a' + n)), nil
	})

	got, err := double.Then(label).Execute(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, "c", got)

	_, err = quiet.Stage("boom", func(context.Context, int) (int, error) {
		panic("boom")
	}).Execute(ctx, 1)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))

	tracer := mocks.NewMockTracer(gomock.NewController(t))
	span := mocks.NewMockSpan(gomock.NewController(t))
	span.EXPECT().End().AnyTimes()
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().SetStatus(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().RecordError(gomock.Any()).AnyTimes()
	tracer.EXPECT().
		Start(gomock.Any(), gomock.Any(), gomock.Any()).
		Times(1).
		Return(ctx, span)
	observed := pipeline.NewBase(decorate.Observability{Tracer: tracer})
	_, err = observed.Stage("traced", func(_ context.Context, n int) (int, error) {
		return n, nil
	}).Execute(ctx, 1)
	require.NoError(t, err)
}

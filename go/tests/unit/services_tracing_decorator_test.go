package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

var errTracingBoom = apperr.Sentinel("tracing boom")

// errServiceMock returns a Service mock whose every CRUD operation fails with
// errTracingBoom, driving the tracing decorator's error-recording branch
// (span.RecordError / SetStatus) that the happy-path mock service cannot reach.
func errServiceMock(
	t *testing.T,
) interfaces.Service[fixtures.TestEntity, fixtures.TestParams, string] {
	t.Helper()
	ctrl := gomock.NewController(t)
	svc := mocks.NewMockService[fixtures.TestEntity, fixtures.TestParams, string](ctrl)
	svc.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, errTracingBoom).AnyTimes()
	svc.EXPECT().
		List(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errTracingBoom).
		AnyTimes()
	svc.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, errTracingBoom).AnyTimes()
	svc.EXPECT().
		Update(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, errTracingBoom).
		AnyTimes()
	svc.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(errTracingBoom).AnyTimes()
	return svc
}

// tracingService builds a tracing-decorated service over base.
func tracingService(
	base interfaces.Service[fixtures.TestEntity, fixtures.TestParams, string],
) interfaces.DecoratedService[fixtures.TestEntity, fixtures.TestParams, string] {
	return decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		base,
		"trace-svc",
	).
		WithTracing(fixtures.NopTracer()).
		Build()
}

// TestServiceDecorator_TracingPassesThroughResults tests that the tracing
// decorator wraps every operation in a span without altering successful
// results.
//
// Why this test is important:
//   - The tracing decorator sits in the production decorator chain; it must be
//     transparent on the happy path (no data mutation, no swallowed results)
//   - Spans must be created per operation for the interceptor->service->repo
//     cascade to be visible in Tempo
//
// What it tests:
//   - Get/List/Create/Update/Delete all succeed and
//     return the underlying service's values unchanged when wrapped in tracing
func TestServiceDecorator_TracingPassesThroughResults(t *testing.T) {
	svc := tracingService(fixtures.StubService(nil, false))
	ctx := context.Background()

	got, err := svc.Get(ctx, "1")
	require.NoError(t, err)
	assert.Equal(t, "1", got.ID)

	_, err = svc.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.NoError(t, err)

	created, err := svc.Create(ctx, &fixtures.TestEntity{ID: "2", Name: "n"})
	require.NoError(t, err)
	assert.Equal(t, "2", created.ID)

	updated, err := svc.Update(ctx, "2", &fixtures.TestEntity{ID: "2", Name: "n2"})
	require.NoError(t, err)
	assert.Equal(t, "n2", updated.Name)

	require.NoError(t, svc.Delete(ctx, "2"))
}

// TestServiceDecorator_TracingRecordsErrors tests that the tracing decorator
// propagates underlying errors (and records them on the span) for every
// operation.
//
// Why this test is important:
//   - A traced operation that fails must still return its error to the caller;
//     the decorator must not swallow or mask failures
//   - The error-recording branch (RecordError/SetStatus) is what makes failed
//     requests diagnosable in Tempo
//
// What it tests:
//   - Get/List/Create/Update/Delete all return the
//     underlying error when the wrapped service fails
func TestServiceDecorator_TracingRecordsErrors(t *testing.T) {
	svc := tracingService(errServiceMock(t))
	ctx := context.Background()

	_, err := svc.Get(ctx, "1")
	require.ErrorIs(t, err, errTracingBoom)

	_, err = svc.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.ErrorIs(t, err, errTracingBoom)

	_, err = svc.Create(ctx, &fixtures.TestEntity{})
	require.ErrorIs(t, err, errTracingBoom)

	_, err = svc.Update(ctx, "1", &fixtures.TestEntity{})
	require.ErrorIs(t, err, errTracingBoom)

	require.ErrorIs(t, svc.Delete(ctx, "1"), errTracingBoom)
}

// TestServiceDecorator_AuthPassesAllOps tests that, when the authorization
// check passes, every operation flows through to the underlying service.
//
// Why this test is important:
//   - The auth decorator must be transparent on the allow path; only the deny
//     path (covered separately) short-circuits. A regression that dropped the
//     inner call would silently no-op writes
//
// What it tests:
//   - List/Create/Update/Delete (and Get) all reach the inner service and
//     succeed when the authorization function returns nil
func TestServiceDecorator_AuthPassesAllOps(t *testing.T) {
	allow := func(context.Context, string) error { return nil }
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "auth-svc",
	).WithAuthorization(allow).Build()
	ctx := context.Background()

	_, err := svc.Get(ctx, "1")
	require.NoError(t, err)
	_, err = svc.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.NoError(t, err)
	_, err = svc.Create(ctx, &fixtures.TestEntity{ID: "2"})
	require.NoError(t, err)
	_, err = svc.Update(ctx, "2", &fixtures.TestEntity{ID: "2"})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctx, "2"))
}

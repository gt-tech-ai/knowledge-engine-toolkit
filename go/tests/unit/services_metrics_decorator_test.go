package unit_test

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// serviceCounter returns the value of the named service counter carrying the given
// operation label, or 0 (found=false) if absent.
func serviceCounter(
	t *testing.T,
	reg *prometheus.Registry,
	name, op string,
) (float64, bool) {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "operation" && l.GetValue() == op {
					return m.GetCounter().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

// TestServiceDecorator_WithMetrics_RecordsOperationsAndErrors tests that the service
// builder's WithMetrics decorator records per-operation counts and errors, reaching
// parity with the repository builder's metrics decorator.
//
// Why this test is important:
//   - Service-layer operations were unobservable via Prometheus (the service builder
//     had no WithMetrics, unlike the repo builder); an operator could not see service
//     op rates or error rates. A silent regression here would remove that signal.
//
// What it tests:
//   - A successful Get increments service_operations_total{operation="Get"} and does
//     NOT increment service_errors_total; a failing Get increments both.
func TestServiceDecorator_WithMetrics_RecordsOperationsAndErrors(t *testing.T) {
	t.Parallel()

	// Success path: one Get, no error.
	regOK := prometheus.NewRegistry()
	ok := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithMetrics(prom.NewFromRegistry(regOK)).Build()

	_, err := ok.Get(context.Background(), "1")
	require.NoError(t, err)

	ops, found := serviceCounter(t, regOK, "service_operations_total", "Get")
	require.True(t, found, "service_operations_total{operation=Get} must be recorded")
	require.InDelta(t, 1.0, ops, 0)
	_, errFound := serviceCounter(t, regOK, "service_errors_total", "Get")
	require.False(t, errFound, "a successful op must not increment the error counter")

	// Failure path: one Get that errors.
	regErr := prometheus.NewRegistry()
	failing := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(apperr.Sentinel("boom"), false), "test",
	).WithMetrics(prom.NewFromRegistry(regErr)).Build()

	_, err = failing.Get(context.Background(), "1")
	require.Error(t, err)

	ops, _ = serviceCounter(t, regErr, "service_operations_total", "Get")
	require.InDelta(t, 1.0, ops, 0, "a failing op still counts as an operation")
	errs, errFound := serviceCounter(t, regErr, "service_errors_total", "Get")
	require.True(t, errFound, "a failing op must increment the error counter")
	require.InDelta(t, 1.0, errs, 0)
}

// TestServiceDecorator_WithMetrics_RecordsEveryOperation tests that the metrics decorator
// records an operation count for each CRUD method, not just Get.
//
// Why this test is important:
//   - Observability parity means EVERY service operation is measurable, not only reads; a
//     decorator that instrumented Get alone would leave writes and custom operations
//     invisible in Prometheus.
//
// What it tests:
//   - Driving List, Create, Update, and Delete through a decorated
//     service records service_operations_total for each under its own operation label.
func TestServiceDecorator_WithMetrics_RecordsEveryOperation(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()
	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithMetrics(prom.NewFromRegistry(reg)).Build()
	ctx := context.Background()

	_, err := svc.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.NoError(t, err)
	_, err = svc.Create(ctx, &fixtures.TestEntity{ID: "1", Name: "a"})
	require.NoError(t, err)
	_, err = svc.Update(ctx, "1", &fixtures.TestEntity{ID: "1", Name: "b"})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctx, "1"))

	for _, op := range []string{"List", "Create", "Update", "Delete"} {
		count, found := serviceCounter(t, reg, "service_operations_total", op)
		require.True(t, found, "operation %s must be counted", op)
		require.InDelta(t, 1.0, count, 0, "operation %s counted exactly once", op)
	}
}

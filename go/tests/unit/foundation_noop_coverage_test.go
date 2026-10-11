package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	foundcache "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cache/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/noop"
	nooptracer "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer/nooptracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// ---------------------------------------------------------------------------
// Noop Metrics - verify all methods are callable and don't panic
// ---------------------------------------------------------------------------

// TestNoopCounter_IncAndAdd tests that the noop counter's Inc and Add methods
// can be called without panicking.
//
// Why this test is important:
//   - Noop metrics are used in test and local-dev environments where a real
//     Prometheus registry is not available; panics here would break all tests
//
// What it tests:
//   - Inc and Add with a label value do not panic
func TestNoopCounter_IncAndAdd(t *testing.T) {
	t.Parallel()

	m := noop.New()
	c := m.Counter("test_total", "help", "label")

	c.Inc("val")
	c.Add(5.0, "val")
}

// TestNoopHistogram_Observe tests that the noop histogram's Observe method can
// be called without panicking.
//
// Why this test is important:
//   - Services call Observe on every request; the noop implementation must
//     silently accept values rather than panic
//
// What it tests:
//   - Observe with a label value does not panic
func TestNoopHistogram_Observe(t *testing.T) {
	t.Parallel()

	m := noop.New()
	h := m.Histogram("test_duration", "help", nil, "label")

	h.Observe(0.5, "val")
}

// TestNoopGauge_SetIncDec tests that the noop gauge's Set, Inc, and Dec
// methods can be called without panicking.
//
// Why this test is important:
//   - Gauges track in-flight requests and queue depths; the noop must accept
//     all operations silently to keep test and dev environments stable
//
// What it tests:
//   - Set, Inc, and Dec with a label value do not panic
func TestNoopGauge_SetIncDec(t *testing.T) {
	t.Parallel()

	m := noop.New()
	g := m.Gauge("test_gauge", "help", "label")

	g.Set(42.0, "val")
	g.Inc("val")
	g.Dec("val")
}

// ---------------------------------------------------------------------------
// Noop Tracer - verify End, SetAttribute, RecordError, SetStatus
// ---------------------------------------------------------------------------

// TestNoopSpan_EndAndAttributes tests that all span methods on the noop tracer
// complete without panicking.
//
// Why this test is important:
//   - All service code wraps operations in spans; the noop span must silently
//     absorb every call in test environments
//
// What it tests:
//   - Start returns a non-nil context and span
//   - SetAttribute, RecordError, SetStatus, and End do not panic
func TestNoopSpan_EndAndAttributes(t *testing.T) {
	t.Parallel()

	tr, err := nooptracer.New()
	require.NoError(t, err, "nooptracer.New() must not return error")

	ctx, span := tr.Start(context.Background(), "test-op")
	require.NotNil(t, ctx, "Start must return non-nil context")

	span.SetAttribute("key", "value")
	span.RecordError(apperr.Sentinel("test error"))
	span.SetStatus(interfaces.SpanStatusOK, "test status")
	span.End()
}

// ---------------------------------------------------------------------------
// Core errors - Join and Unwrap
// ---------------------------------------------------------------------------

// TestCoreErrors_Join tests that the apperr.Join re-export correctly combines
// multiple errors into one.
//
// Why this test is important:
//   - The re-export keeps callers from importing the stdlib "errors" package
//     alongside the app errors package; a broken re-export would break callers
//
// What it tests:
//   - Join of two non-nil errors returns a non-nil combined error
func TestCoreErrors_Join(t *testing.T) {
	t.Parallel()

	e1 := apperr.Sentinel("first")
	e2 := apperr.Sentinel("second")

	joined := apperr.Join(e1, e2)
	require.Error(t, joined, "Join of two errors must not return nil")
}

// TestCoreErrors_Unwrap tests that the apperr.Unwrap re-export correctly
// exposes the directly wrapped error.
//
// Why this test is important:
//   - The re-export keeps callers from importing stdlib errors alongside the
//     app errors package; a broken re-export would break error unwrapping in
//     service error handlers
//
// What it tests:
//   - Unwrap on a wrapped error returns the inner error
//   - The inner error message matches the original
func TestCoreErrors_Unwrap(t *testing.T) {
	t.Parallel()

	inner := apperr.Sentinel("inner")
	wrapped := apperr.Wrap(inner, apperr.ErrInternal, "outer")

	unwrapped := apperr.Unwrap(wrapped)
	require.Error(t, unwrapped, "Unwrap must return non-nil for a wrapped error")
	assert.Equal(t, "inner", unwrapped.Error())
}

// ---------------------------------------------------------------------------
// Foundation cache decorators - Builder + metricsDecorator
// ---------------------------------------------------------------------------

// newMapByteCache builds a MockByteCache backed by an in-memory map, so Get after
// Set round-trips and Delete removes the key — the stateful behaviour the cache
// decorator's hit/miss/delete paths exercise. Get/Set/Delete are each registered
// exactly once with AnyTimes because the decorator calls them an arbitrary number
// of times.
func newMapByteCache(ctrl *gomock.Controller) *mocks.MockByteCache {
	store := make(map[string][]byte)
	c := mocks.NewMockByteCache(ctrl)
	c.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, key string) ([]byte, bool) {
			v, ok := store[key]
			return v, ok
		},
	).AnyTimes()
	c.EXPECT().Set(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Do(
		func(_ context.Context, key string, value []byte, _ time.Duration) {
			store[key] = value
		},
	).AnyTimes()
	c.EXPECT().Delete(gomock.Any(), gomock.Any()).Do(
		func(_ context.Context, key string) {
			delete(store, key)
		},
	).AnyTimes()
	return c
}

// TestFoundationCacheDecorator_BuilderNoMetrics tests that the decorator
// builder returns a functional cache when no decorators are applied.
//
// Why this test is important:
//   - Services that skip metrics must still read/write correctly through the
//     builder; a broken pass-through would silently lose data
//
// What it tests:
//   - Build() with no decorators returns a non-nil cache
//   - Set followed by Get returns the stored value with ok=true
func TestFoundationCacheDecorator_BuilderNoMetrics(t *testing.T) {
	t.Parallel()

	base := newMapByteCache(gomock.NewController(t))
	decorated := foundcache.NewBuilder(base, "test-cache").Build()
	require.NotNil(t, decorated, "Build() must return non-nil cache")

	ctx := context.Background()
	decorated.Set(ctx, "key", []byte("val"), time.Minute)
	v, ok := decorated.Get(ctx, "key")
	assert.True(t, ok, "Get after Set must return ok=true")
	assert.Equal(t, "val", string(v))
}

// TestFoundationCacheDecorator_BuilderWithMetrics tests that the metrics
// decorator wraps a real cache correctly, exercising hit, miss, and delete
// paths so all metricsDecorator branches are covered.
//
// Why this test is important:
//   - The metrics decorator records hit/miss/delete events; a broken decorator
//     would silently corrupt cache operations while recording wrong metrics
//
// What it tests:
//   - Set followed by Get returns the stored value (hit path)
//   - Get on a missing key returns ok=false (miss path)
//   - Delete removes the key so subsequent Get returns ok=false (delete path)
func TestFoundationCacheDecorator_BuilderWithMetrics(t *testing.T) {
	t.Parallel()

	base := newMapByteCache(gomock.NewController(t))
	m := noop.New()

	decorated := foundcache.NewBuilder(base, "test-cache").
		WithMetrics(m).
		Build()
	require.NotNil(t, decorated, "Build() must return non-nil cache")

	ctx := context.Background()

	// Set + Get (hit) - exercises metricsDecorator.Set and .Get hit path.
	decorated.Set(ctx, "key", []byte("val"), time.Minute)
	v, ok := decorated.Get(ctx, "key")
	assert.True(t, ok, "Get after Set must return ok=true")
	assert.Equal(t, "val", string(v))

	// Get (miss) - exercises metricsDecorator.Get miss path.
	_, ok = decorated.Get(ctx, "missing")
	assert.False(t, ok, "Get on missing key must return ok=false")

	// Delete - exercises metricsDecorator.Delete.
	decorated.Delete(ctx, "key")
	_, ok = decorated.Get(ctx, "key")
	assert.False(t, ok, "Get after Delete must return ok=false")
}

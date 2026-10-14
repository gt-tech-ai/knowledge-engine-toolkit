package unit_test

import (
	"context"
	"testing"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/messaging"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/replaybuffer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// clientOpCounter returns client_operations_total for the operation op in reg.
func clientOpCounter(t *testing.T, reg *promclient.Registry, op string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "client_operations_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "operation" && l.GetValue() == op {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	require.Failf(t, "counter not found", "client_operations_total{operation=%q}", op)
	return 0
}

// countingRetrier returns a Retrier mock that runs op up to attempts times, stopping at
// the first success.
func countingRetrier(ctrl *gomock.Controller, attempts int) *mocks.MockRetrier {
	r := mocks.NewMockRetrier(ctrl)
	r.EXPECT().Retry(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, op func() error) error {
			var err error
			for range attempts {
				if err = op(); err == nil {
					return nil
				}
			}
			return err
		},
	).AnyTimes()
	return r
}

// TestClientTier_OrdersBulkheadOutermost tests the client decoration tier.
//
// Why this test is important:
//   - The bulkhead must bound calls before a retry multiplies them, and each retry
//     attempt must pass the breaker and get its own deadline, span and metrics; any
//     other order sheds load wrongly or hides attempts.
//
// What it tests:
//   - The tier is named "client", traces with client spans, and orders bulkhead →
//     retry → circuit breaker → timeout → tracing → metrics → logging.
func TestClientTier_OrdersBulkheadOutermost(t *testing.T) {
	t.Parallel()
	assert.Equal(
		t,
		decorate.Tier{
			Name:     "client",
			SpanKind: interfaces.SpanKindClient,
			Order: []decorate.Layer{
				decorate.LayerBulkhead,
				decorate.LayerRetry,
				decorate.LayerCircuitBreaker,
				decorate.LayerTimeout,
				decorate.LayerTracing,
				decorate.LayerMetrics,
				decorate.LayerLogging,
			},
		},
		client.Tier(),
	)
}

// TestClientBase_RetriesOnlyIdempotentOperations tests Exec, Once, Do and DoOnce.
//
// Why this test is important:
//   - Replaying a non-idempotent call (a create, an upload whose body is consumed)
//     duplicates or corrupts data; an idempotent read must recover from a transient
//     failure.
//
// What it tests:
//   - Exec and Do retry a transient failure to success and return the value.
//   - Once and DoOnce run the operation exactly once and return its error.
//   - Every attempt passes through the bulkhead and is counted under its operation.
//   - The zero Base runs the operation bare.
func TestClientBase_RetriesOnlyIdempotentOperations(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	bh := mocks.NewMockBulkhead(ctrl)
	bh.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, fn func() error) error { return fn() },
	).Times(4)
	reg := promclient.NewRegistry()
	b := client.NewBase("svc", decorate.Deps{
		Observability: decorate.Observability{Metrics: prom.NewFromRegistry(reg)},
		Retrier:       countingRetrier(ctrl, 3),
		Bulkhead:      bh,
	})
	transient := apperr.New(apperr.CodeUnavailable, "blip")

	calls := 0
	got, err := b.Exec(t.Context(), "get", func(context.Context) (int, error) {
		calls++
		if calls < 2 {
			return 0, transient
		}
		return 42, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 42, got)
	assert.InDelta(t, 2.0, clientOpCounter(t, reg, "get"), 0)

	calls = 0
	require.NoError(t, b.Do(t.Context(), "put", func(context.Context) error {
		calls++
		if calls < 3 {
			return transient
		}
		return nil
	}))
	assert.Equal(t, 3, calls)

	calls = 0
	_, err = b.Once(t.Context(), "create", func(context.Context) (int, error) {
		calls++
		return 0, transient
	})
	require.ErrorIs(t, err, transient)
	assert.Equal(t, 1, calls)

	calls = 0
	err = b.DoOnce(t.Context(), "upload", func(context.Context) error {
		calls++
		return transient
	})
	require.ErrorIs(t, err, transient)
	assert.Equal(t, 1, calls)

	bare, err := client.Base{}.Once(
		t.Context(),
		"x",
		func(context.Context) (string, error) {
			return "bare", nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "bare", bare)
}

// TestClientBase_StreamDeadlineOutlivesTheOpen tests Stream.
//
// Why this test is important:
//   - A download returns a body read after the call returns; a deadline cancelled on
//     return would abort every read, and one never released would leak a timer per
//     stream.
//
// What it tests:
//   - The stream's context carries the client timeout and stays live after Stream
//     returns; release cancels it.
//   - A failed open returns its error with the context already released.
func TestClientBase_StreamDeadlineOutlivesTheOpen(t *testing.T) {
	t.Parallel()
	b := client.NewBase("svc", decorate.Deps{Timeout: time.Minute})

	var streamCtx context.Context
	got, release, err := b.Stream(t.Context(), "download",
		func(ctx context.Context) (string, error) {
			streamCtx = ctx //nolint:fatcontext // captured for inspection
			return "body", nil
		})
	require.NoError(t, err)
	assert.Equal(t, "body", got)
	_, hasDeadline := streamCtx.Deadline()
	assert.True(t, hasDeadline)
	require.NoError(t, streamCtx.Err())
	release()
	require.ErrorIs(t, streamCtx.Err(), context.Canceled)

	boom := apperr.New(apperr.CodeUnavailable, "down")
	_, release, err = b.Stream(t.Context(), "download",
		func(ctx context.Context) (string, error) {
			streamCtx = ctx //nolint:fatcontext // captured for inspection
			return "", boom
		})
	require.ErrorIs(t, err, boom)
	require.ErrorIs(t, streamCtx.Err(), context.Canceled)
	release()
}

// TestClientDecorate_FamiliesCarryEveryResult tests the per-family Decorate wrappers.
//
// Why this test is important:
//   - Each family carries multi-value results (a lock's token and flag, a replay's tail
//     and completeness, a connector page and its token) through the single-value tier;
//     a dropped value silently breaks fencing, replay or pagination.
//
// What it tests:
//   - lock Acquire/Renew/Release, replaybuffer ReplayAfter/Append/Prune/Delete,
//     connector ListPage/TestConnection and messaging Publish/PublishBatch return the
//     base's values and errors unchanged.
//   - storage Download hands back a body whose Close also releases the stream.
//   - analytics Compact on a store without a compactor is CodeInvalidInput.
func TestClientDecorate_FamiliesCarryEveryResult(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	ctx := t.Context()
	deps := decorate.Deps{Timeout: time.Minute}

	baseLock := mocks.NewMockDistributedLock(ctrl)
	baseLock.EXPECT().Acquire(gomock.Any(), "k").Return("tok", true, nil)
	baseLock.EXPECT().Renew(gomock.Any(), "k", "tok").Return(true, nil)
	baseLock.EXPECT().Release(gomock.Any(), "k", "tok").Return(nil)
	lk := lock.Decorate(baseLock, "lock", deps)
	token, acquired, err := lk.Acquire(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, "tok", token)
	assert.True(t, acquired)
	held, err := lk.Renew(ctx, "k", "tok")
	require.NoError(t, err)
	assert.True(t, held)
	require.NoError(t, lk.Release(ctx, "k", "tok"))

	tail := []interfaces.BufferedMessage{{ID: "m2"}}
	baseBuf := mocks.NewMockReplayBuffer(ctrl)
	baseBuf.EXPECT().ReplayAfter(gomock.Any(), "k", "m1").Return(tail, true, nil)
	baseBuf.EXPECT().Append(gomock.Any(), "k", "m3", []byte("x")).Return(nil)
	baseBuf.EXPECT().Prune(gomock.Any(), "k", "m2").Return(nil)
	baseBuf.EXPECT().Delete(gomock.Any(), "k").Return(nil)
	buf := replaybuffer.Decorate(baseBuf, "replay", deps)
	msgs, complete, err := buf.ReplayAfter(ctx, "k", "m1")
	require.NoError(t, err)
	assert.Equal(t, tail, msgs)
	assert.True(t, complete)
	require.NoError(t, buf.Append(ctx, "k", "m3", []byte("x")))
	require.NoError(t, buf.Prune(ctx, "k", "m2"))
	require.NoError(t, buf.Delete(ctx, "k"))

	objects := []interfaces.StorageObject{{Key: "a"}}
	baseSrc := mocks.NewMockConnectorSource(ctrl)
	baseSrc.EXPECT().ListPage(gomock.Any(), "t1", 10).Return(objects, "t2", nil)
	baseSrc.EXPECT().TestConnection(gomock.Any()).Return(7, nil)
	src := connector.Decorate(baseSrc, "connector", deps)
	page, next, err := src.ListPage(ctx, "t1", 10)
	require.NoError(t, err)
	assert.Equal(t, objects, page)
	assert.Equal(t, "t2", next)
	n, err := src.TestConnection(ctx)
	require.NoError(t, err)
	assert.Equal(t, 7, n)

	boom := apperr.New(apperr.CodeUnavailable, "down")
	basePub := mocks.NewMockMessagePublisher(ctrl)
	basePub.EXPECT().Publish(gomock.Any(), "topic", []byte("p")).Return(boom)
	basePub.EXPECT().
		PublishBatch(gomock.Any(), "topic", [][]byte{[]byte("p")}).
		Return(nil)
	pub := messaging.Decorate(basePub, "publisher", deps)
	require.ErrorIs(t, pub.Publish(ctx, "topic", []byte("p")), boom)
	require.NoError(t, pub.PublishBatch(ctx, "topic", [][]byte{[]byte("p")}))

	body := mocks.NewMockReadCloser(ctrl)
	body.EXPECT().Close().Return(nil)
	baseStorage := mocks.NewMockStorageClient(ctrl)
	baseStorage.EXPECT().Download(gomock.Any(), "b", "k").Return(body, nil)
	rc, err := storage.Decorate(baseStorage, "storage", deps).Download(ctx, "b", "k")
	require.NoError(t, err)
	require.NoError(t, rc.Close())

	store := analytics.Decorate(mocks.NewMockAnalyticsStore(ctrl), "analytics", deps)
	err = store.(interfaces.AnalyticsCompactor).Compact(
		ctx, "cube", types.Grain("hour"), "org", time.Unix(0, 0),
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// clientCounter returns the counter name{label=value} in reg (0 when absent).
func clientCounter(
	t *testing.T,
	reg *promclient.Registry,
	name, label, value string,
) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestClientDecorate_CacheIsFailOpenAndCountsHits tests the cache family's decoration.
//
// Why this test is important:
//   - A cache sits on the hot path and must never fail a request: when its breaker is
//     open the caller falls through to the source of truth, and the hit ratio is the
//     one signal that says the cache is earning its keep.
//   - Invalidation is a capability callers detect by type assertion; decoration must not
//     hide it, or a grant change would leave stale authorization cached.
//
// What it tests:
//   - A present key is a hit and an absent one a miss (cache_hits_total and
//     cache_misses_total labelled by cache).
//   - With the breaker refusing, Get is a miss and Set is dropped without reaching the
//     backend.
//   - A backend that can invalidate stays a CacheInvalidator after decoration, and its
//     errors come back; one that cannot is not.
func TestClientDecorate_CacheIsFailOpenAndCountsHits(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	ctx := t.Context()
	reg := promclient.NewRegistry()
	obs := decorate.Observability{Metrics: prom.NewFromRegistry(reg)}

	base := mocks.NewMockByteCache(ctrl)
	base.EXPECT().Get(gomock.Any(), "present").Return([]byte("v"), true)
	base.EXPECT().Get(gomock.Any(), "absent").Return(nil, false)
	c := cache.Decorate(base, "authz", decorate.Deps{Observability: obs})
	v, hit := c.Get(ctx, "present")
	assert.True(t, hit)
	assert.Equal(t, []byte("v"), v)
	_, hit = c.Get(ctx, "absent")
	assert.False(t, hit)
	assert.InDelta(t, 1.0, clientCounter(t, reg, "cache_hits_total", "cache", "authz"), 0)
	assert.InDelta(
		t,
		1.0,
		clientCounter(t, reg, "cache_misses_total", "cache", "authz"),
		0,
	)
	_, ok := c.(interfaces.CacheInvalidator)
	assert.False(t, ok, "a backend without invalidation stays without it")

	open := mocks.NewMockCircuitBreaker(ctrl)
	open.EXPECT().Execute(gomock.Any()).
		Return(apperr.New(apperr.CodeUnavailable, "breaker open")).Times(2)
	refused := cache.Decorate(mocks.NewMockByteCache(ctrl), "authz", decorate.Deps{
		Breaker: open,
	})
	_, hit = refused.Get(ctx, "present")
	assert.False(t, hit)
	refused.Set(ctx, "k", []byte("v"), time.Minute)

	inv := mocks.NewMockCacheInvalidator(ctrl)
	boom := apperr.New(apperr.CodeUnavailable, "scan failed")
	inv.EXPECT().InvalidatePrefix(gomock.Any(), "user:").Return(boom)
	inv.EXPECT().InvalidateAll(gomock.Any()).Return(nil)
	both := struct {
		*mocks.MockByteCache
		*mocks.MockCacheInvalidator
	}{mocks.NewMockByteCache(ctrl), inv}
	decorated := cache.Decorate(both, "authz", decorate.Deps{})
	invalidating, ok := decorated.(interfaces.CacheInvalidator)
	require.True(t, ok)
	require.ErrorIs(t, invalidating.InvalidatePrefix(ctx, "user:"), boom)
	require.NoError(t, invalidating.InvalidateAll(ctx))
}

// TestClientDecorate_SecretsAndHandlersAreObserved tests the credential source and the
// message-handler decoration.
//
// Why this test is important:
//   - A credential read and each handled message are external work an operator must be
//     able to count and trace; a decoration that dropped a value or an error would
//     hand callers a blank secret or ack a failed message.
//
// What it tests:
//   - secrets Get returns the backend's value and Put its error, each counted as a
//     client operation.
//   - A handler run by a consumer's handler tier is counted under its topic, and its
//     error comes back unchanged after one call, even as a retryable operation over
//     deps that carry a retrier (the broker's redelivery is the retry).
func TestClientDecorate_SecretsAndHandlersAreObserved(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	ctx := t.Context()
	reg := promclient.NewRegistry()
	obs := decorate.Observability{Metrics: prom.NewFromRegistry(reg)}

	ref := types.Ref{Env: "dev", Class: "bootstrap", Field: "token"}
	boom := apperr.New(apperr.CodeForbidden, "denied")
	base := mocks.NewMockSource(ctrl)
	base.EXPECT().Get(gomock.Any(), ref).Return(types.NewSecret("s3cr3t"), nil)
	base.EXPECT().Put(gomock.Any(), ref, gomock.Any()).Return(boom)
	src := secrets.Decorate(base, "secrets", decorate.Deps{Observability: obs})
	got, err := src.Get(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", got.Reveal())
	require.ErrorIs(t, src.Put(ctx, ref, types.NewSecret("x")), boom)
	assert.InDelta(t, 1.0, clientOpCounter(t, reg, "get"), 0)
	assert.InDelta(t, 1.0, clientOpCounter(t, reg, "put"), 0)

	calls := 0
	handlers := client.NewHandlerBase("messaging.handler", decorate.Deps{
		Observability: obs,
		Retrier:       countingRetrier(ctrl, 3),
		Timeout:       time.Minute,
	})
	err = handlers.Do(ctx, "orders", func(context.Context) error {
		calls++
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, calls)
	assert.InDelta(t, 1.0, clientOpCounter(t, reg, "orders"), 0)
}

// TestClientBase_MessagingSpansCarryTheirRole tests the span kinds of the messaging
// bases.
//
// Why this test is important:
//   - A trace backend draws a publish → handle hop as an async producer → consumer
//     edge only from the span kinds; CLIENT spans on both sides read as two unrelated
//     outbound calls.
//
// What it tests:
//   - NewBase opens CLIENT spans, NewProducerBase PRODUCER spans and NewHandlerBase
//     CONSUMER spans, each named "<name>.<op>".
func TestClientBase_MessagingSpansCarryTheirRole(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	tracer := mocks.NewMockTracer(ctrl)
	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().End().Times(3)
	kinds := map[string]interfaces.SpanKind{}
	tracer.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any()).Times(3).DoAndReturn(
		func(
			ctx context.Context, name string, opts ...interfaces.SpanOption,
		) (context.Context, interfaces.Span) {
			var cfg interfaces.SpanConfig
			for _, opt := range opts {
				opt(&cfg)
			}
			kinds[name] = cfg.Kind
			return ctx, span
		})
	deps := decorate.Deps{Observability: decorate.Observability{Tracer: tracer}}
	noop := func(context.Context) error { return nil }

	require.NoError(t, client.NewBase("store", deps).Do(t.Context(), "put", noop))
	require.NoError(
		t,
		client.NewProducerBase("bus", deps).Do(t.Context(), "orders", noop),
	)
	require.NoError(
		t,
		client.NewHandlerBase("bus.handler", deps).Do(t.Context(), "orders", noop),
	)
	assert.Equal(t, map[string]interfaces.SpanKind{
		"store.put":          interfaces.SpanKindClient,
		"bus.orders":         interfaces.SpanKindProducer,
		"bus.handler.orders": interfaces.SpanKindConsumer,
	}, kinds)
}

// TestClientBase_OpenBoundsOnlyTheOpen tests opening a long-lived stream.
//
// Why this test is important:
//   - A generation stream can outlive any per-call budget, so the client's timeout must
//     bound how long the open takes but must not put a deadline on the stream; and an
//     open that overruns or fails must be torn down, not leaked.
//
// What it tests:
//   - An open within the timeout returns its stream on a context with no deadline that
//     stays live until the caller's context ends.
//   - An open that overruns the timeout is CodeTimeout and its context is canceled.
//   - A failed open returns its own error and its context is canceled.
func TestClientBase_OpenBoundsOnlyTheOpen(t *testing.T) {
	t.Parallel()
	base := client.NewBase("stream", decorate.Deps{Timeout: 20 * time.Millisecond})
	ready := func(ctx context.Context) (context.Context, error) { return ctx, nil }

	t.Run("within the timeout", func(t *testing.T) {
		t.Parallel()
		parent, cancel := context.WithCancel(t.Context())
		streamCtx, err := base.Open(parent, "Generate", ready)
		require.NoError(t, err)
		_, hasDeadline := streamCtx.Deadline()
		assert.False(t, hasDeadline, "the stream carries no deadline of the client's")
		time.Sleep(40 * time.Millisecond)
		require.NoError(t, streamCtx.Err(), "the stream outlives the open timeout")
		cancel()
		<-streamCtx.Done()
	})

	t.Run("overrun", func(t *testing.T) {
		t.Parallel()
		canceled := make(chan struct{})
		_, err := base.Open(
			t.Context(),
			"Generate",
			func(ctx context.Context) (struct{}, error) {
				<-ctx.Done()
				close(canceled)
				return struct{}{}, ctx.Err()
			},
		)
		assert.Equal(t, apperr.CodeTimeout, apperr.Code(err))
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("the abandoned open was not canceled")
		}
	})

	t.Run("failed open", func(t *testing.T) {
		t.Parallel()
		boom := apperr.New(apperr.CodeUpstream, "refused")
		opened := make(chan context.Context, 1)
		_, err := base.Open(
			t.Context(),
			"Generate",
			func(ctx context.Context) (struct{}, error) {
				opened <- ctx
				return struct{}{}, boom
			},
		)
		require.ErrorIs(t, err, boom)
		assert.Error(t, (<-opened).Err(), "the failed open's context is canceled")
	})
}

package unit_test

import (
	"context"
	"testing"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_model/go"
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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// clientOpCounter returns client_operations_total for the operation op in reg.
func clientOpCounter(t *testing.T, reg *promclient.Registry, op string) float64 {
	t.Helper()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	v, ok := counterByLabel(mfs, "client_operations_total", "operation", op)
	if !ok {
		require.Failf(t, "counter not found", "client_operations_total{operation=%q}", op)
	}

	return v
}

// counterByLabel finds the named counter's series carrying label=value.
func counterByLabel(
	mfs []*io_prometheus_client.MetricFamily,
	name, label, value string,
) (float64, bool) {
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}

		if v, ok := familyCounter(mf, label, value); ok {
			return v, true
		}
	}

	return 0, false
}

// familyCounter finds the family's series carrying label=value.
func familyCounter(
	mf *io_prometheus_client.MetricFamily, label, value string,
) (float64, bool) {
	for _, m := range mf.GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == label && l.GetValue() == value {
				return m.GetCounter().GetValue(), true
			}
		}
	}

	return 0, false
}

// countingRetrier returns a Retrier mock that runs op up to attempts times, stopping at
// the first success.
func countingRetrier(ctrl *gomock.Controller, attempts int) *mocks.MockRetrier {
	r := mocks.NewMockRetrier(ctrl)
	r.EXPECT().Retry(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, op func() error) error {
			var err error

			for range attempts {
				err = op()
				if err == nil {
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
	).Times(timesArg4)

	reg := promclient.NewRegistry()
	b := wireBase(textSvc, &decorate.Deps{
		Metrics:  prom.NewFromRegistry(reg),
		Retrier:  countingRetrier(ctrl, 3),
		Bulkhead: bh,
	})
	transient := apperr.New(apperr.CodeUnavailable, "blip")

	calls := 0

	testClientBaseRetriesOnlyIdempotentOperationsRest(t, reg, &b, transient, calls)
}

// testClientBaseRetriesOnlyIdempotentOperationsRest continues
// TestClientBase_RetriesOnlyIdempotentOperations with its remaining steps.
func testClientBaseRetriesOnlyIdempotentOperationsRest(
	t *testing.T,
	reg *promclient.Registry,
	b *client.Base,
	transient *apperr.AppError,
	calls int,
) {
	t.Helper()

	got, err := b.Exec(t.Context(), textGet, func(context.Context) (int, error) {
		calls++
		if calls < 2 {
			return 0, transient
		}

		return 42, nil
	})
	require.NoError(t, err)
	assert.Equal(t, wantGot42, got)
	assert.InDelta(t, wantClientOpCounter2p0, clientOpCounter(t, reg, textGet), 0)

	calls = 0

	require.NoError(t, b.Do(t.Context(), textPut, func(context.Context) error {
		calls++
		if calls < 3 {
			return transient
		}

		return nil
	}))
	assert.Equal(t, wantCalls3, calls)

	calls = 0
	_, err = b.ExecOnce(t.Context(), textCreate, func(context.Context) (int, error) {
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

	bare, err := (&client.Base{}).ExecOnce(
		t.Context(),
		textX,
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

	b := wireBase(textSvc, &decorate.Deps{Timeout: time.Minute})

	var streamCtx context.Context

	got, release, err := b.Stream(t.Context(), "download",
		func(ctx context.Context) (string, error) {
			streamCtx = ctx //nolint:fatcontext // captured for inspection

			return textBody, nil
		})
	require.NoError(t, err)
	assert.Equal(t, textBody, got)

	_, hasDeadline := streamCtx.Deadline()
	assert.True(t, hasDeadline)
	require.NoError(t, streamCtx.Err())
	release()
	require.ErrorIs(t, streamCtx.Err(), context.Canceled)

	boom := apperr.New(apperr.CodeUnavailable, textDown)

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
	baseLock.EXPECT().Acquire(gomock.Any(), textK).Return(textTok, true, nil)
	baseLock.EXPECT().Renew(gomock.Any(), textK, textTok).Return(true, nil)
	baseLock.EXPECT().Release(gomock.Any(), textK, textTok).Return(nil)

	lk := fixtures.Must(lock.Decorate(baseLock, decorate.Fixed(&deps), "lock"))
	token, acquired, err := lk.Acquire(ctx, textK)
	require.NoError(t, err)
	assert.Equal(t, textTok, token)
	assert.True(t, acquired)

	held, err := lk.Renew(ctx, textK, textTok)
	require.NoError(t, err)
	assert.True(t, held)
	require.NoError(t, lk.Release(ctx, textK, textTok))

	tail := []interfaces.BufferedMessage{{ID: "m2"}}

	testClientDecorateFamiliesCarryEveryResultRest2(ctx, t, ctrl, &deps, tail)
}

// testClientDecorateFamiliesCarryEveryResultRest2 continues
// TestClientDecorate_FamiliesCarryEveryResult with its remaining steps.
func testClientDecorateFamiliesCarryEveryResultRest2(
	ctx context.Context,
	t *testing.T,
	ctrl *gomock.Controller,
	deps *decorate.Deps,
	tail []interfaces.BufferedMessage,
) {
	t.Helper()

	var err error

	baseBuf := mocks.NewMockReplayBuffer(ctrl)
	baseBuf.EXPECT().ReplayAfter(gomock.Any(), textK, textM1).Return(tail, true, nil)
	baseBuf.EXPECT().Append(gomock.Any(), textK, "m3", []byte(textX)).Return(nil)
	baseBuf.EXPECT().Prune(gomock.Any(), textK, "m2").Return(nil)
	baseBuf.EXPECT().Delete(gomock.Any(), textK).Return(nil)

	buf := fixtures.Must(replaybuffer.Decorate(baseBuf, decorate.Fixed(deps), "replay"))
	msgs, complete, err := buf.ReplayAfter(ctx, textK, textM1)
	require.NoError(t, err)
	assert.Equal(t, tail, msgs)
	assert.True(t, complete)
	require.NoError(t, buf.Append(ctx, textK, "m3", []byte(textX)))
	require.NoError(t, buf.Prune(ctx, textK, "m2"))
	require.NoError(t, buf.Delete(ctx, textK))

	objects := []interfaces.StorageObject{{Key: textA}}
	baseSrc := mocks.NewMockConnectorSource(ctrl)
	baseSrc.EXPECT().ListPage(gomock.Any(), "t1", 10).Return(objects, "t2", nil)
	baseSrc.EXPECT().TestConnection(gomock.Any()).Return(rets7, nil)

	src := fixtures.Must(connector.Decorate(baseSrc, decorate.Fixed(deps), "connector"))
	page, next, err := src.ListPage(ctx, "t1", limit10)
	require.NoError(t, err)
	assert.Equal(t, objects, page)
	assert.Equal(t, "t2", next)

	testClientDecorateFamiliesCarryEveryResultRest(ctx, t, ctrl, deps, src)
}

// testClientDecorateFamiliesCarryEveryResultRest continues
// TestClientDecorate_FamiliesCarryEveryResult with its remaining steps.
func testClientDecorateFamiliesCarryEveryResultRest(
	ctx context.Context,
	t *testing.T,
	ctrl *gomock.Controller,
	deps *decorate.Deps,
	src interfaces.ConnectorSource,
) {
	t.Helper()

	var err error

	n, err := src.TestConnection(ctx)
	require.NoError(t, err)
	assert.Equal(t, equalArg7, n)

	boom := apperr.New(apperr.CodeUnavailable, textDown)
	basePub := mocks.NewMockMessagePublisher(ctrl)
	basePub.EXPECT().Publish(gomock.Any(), "topic", []byte(textP)).Return(boom)
	basePub.EXPECT().
		PublishBatch(gomock.Any(), "topic", [][]byte{[]byte(textP)}).
		Return(nil)

	pub := fixtures.Must(
		messaging.Decorate(basePub, decorate.Fixed(deps), "publisher", client.Producer()),
	)
	require.ErrorIs(t, pub.Publish(ctx, "topic", []byte(textP)), boom)
	require.NoError(t, pub.PublishBatch(ctx, "topic", [][]byte{[]byte(textP)}))

	body := mocks.NewMockReadCloser(ctrl)
	body.EXPECT().Close().Return(nil)

	baseStorage := mocks.NewMockStorageClient(ctrl)
	baseStorage.EXPECT().Download(gomock.Any(), textB, textK).Return(body, nil)

	rc, err := fixtures.Must(storage.Decorate(baseStorage, decorate.Fixed(deps), "storage")).
		Download(ctx, textB, textK)
	require.NoError(t, err)
	require.NoError(t, rc.Close())

	store := fixtures.Must(
		analytics.Decorate(
			mocks.NewMockAnalyticsStore(ctrl),
			decorate.Fixed(deps),
			textAnalytics,
		),
	)

	err = asType[interfaces.AnalyticsCompactor](t, store).Compact(
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

	v, _ := counterByLabel(mfs, name, label, value)

	return v
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
	base.EXPECT().Get(gomock.Any(), "present").Return([]byte(textV), true)
	base.EXPECT().Get(gomock.Any(), textAbsent).Return(nil, false)

	c := fixtures.Must(
		cache.Decorate(
			base,
			decorate.Fixed(&decorate.Deps{Observability: obs}),
			textAuthz,
		),
	)
	v, hit := c.Get(ctx, "present")
	assert.True(t, hit)
	assert.Equal(t, []byte(textV), v)

	_, hit = c.Get(ctx, textAbsent)
	assert.False(t, hit)
	assert.InDelta(
		t,
		wantClientCounter1p0,
		clientCounter(t, reg, "cache_hits_total", "cache", textAuthz),
		0,
	)
	assert.InDelta(
		t,
		wantClientCounter1p0,
		clientCounter(t, reg, "cache_misses_total", "cache", textAuthz),
		0,
	)

	_, ok := c.(interfaces.CacheInvalidator)
	assert.False(t, ok, "a backend without invalidation stays without it")

	testClientDecorateCacheIsFailOpenAndCountsHitsRest(ctx, t, ctrl)
}

// testClientDecorateCacheIsFailOpenAndCountsHitsRest continues
// TestClientDecorate_CacheIsFailOpenAndCountsHits with its remaining steps.
func testClientDecorateCacheIsFailOpenAndCountsHitsRest(
	ctx context.Context, t *testing.T, ctrl *gomock.Controller,
) {
	t.Helper()

	var ok bool

	var hit bool

	open := mocks.NewMockCircuitBreaker(ctrl)
	open.EXPECT().Execute(gomock.Any()).
		Return(apperr.New(apperr.CodeUnavailable, "breaker open")).Times(timesArg2)

	refused := fixtures.Must(
		cache.Decorate(mocks.NewMockByteCache(ctrl), decorate.Fixed(&decorate.Deps{
			Breaker: open,
		}), textAuthz),
	)

	_, hit = refused.Get(ctx, "present")
	assert.False(t, hit)
	refused.Set(ctx, textK, []byte(textV), time.Minute)

	inv := mocks.NewMockCacheInvalidator(ctrl)
	boom := apperr.New(apperr.CodeUnavailable, "scan failed")
	inv.EXPECT().InvalidatePrefix(gomock.Any(), "user:").Return(boom)
	inv.EXPECT().InvalidateAll(gomock.Any()).Return(nil)

	both := struct {
		*mocks.MockByteCache
		*mocks.MockCacheInvalidator
	}{mocks.NewMockByteCache(ctrl), inv}
	decorated := fixtures.Must(
		cache.Decorate(both, decorate.Fixed(&decorate.Deps{}), textAuthz),
	)
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

	ref := types.Ref{Env: textDev, Class: textBootstrap, Field: "token"}
	boom := apperr.New(apperr.CodeForbidden, "denied")
	base := mocks.NewMockSource(ctrl)
	base.EXPECT().Get(gomock.Any(), ref).Return(types.NewSecret("s3cr3t"), nil)
	base.EXPECT().Put(gomock.Any(), ref, gomock.Any()).Return(boom)

	src := fixtures.Must(
		secrets.Decorate(
			base,
			decorate.Fixed(&decorate.Deps{Observability: obs}),
			"secrets",
		),
	)
	got, err := src.Get(ctx, ref)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", got.Reveal())
	require.ErrorIs(t, src.Put(ctx, ref, types.NewSecret(textX)), boom)
	assert.InDelta(t, wantClientOpCounter1p0, clientOpCounter(t, reg, textGet), 0)
	assert.InDelta(t, wantClientOpCounter1p0, clientOpCounter(t, reg, textPut), 0)

	calls := 0
	handlers := wireBase("messaging.handler", &decorate.Deps{
		Observability: obs,
		Retrier:       countingRetrier(ctrl, 3),
		Timeout:       time.Minute,
	}, client.Handler())

	err = handlers.Do(ctx, textOrders, func(context.Context) error {
		calls++

		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, calls)
	assert.InDelta(t, wantClientOpCounter1p0, clientOpCounter(t, reg, textOrders), 0)
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
//   - A plain base opens CLIENT spans, a client.Producer() base PRODUCER spans
//     and a client.Handler() base CONSUMER spans, each named "<name>.<op>".
func TestClientBase_MessagingSpansCarryTheirRole(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	tracer := mocks.NewMockTracer(ctrl)
	span := mocks.NewMockSpan(ctrl)
	span.EXPECT().SetAttribute(gomock.Any(), gomock.Any()).AnyTimes()
	span.EXPECT().End().Times(timesArg3)

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

	deps := decorate.Deps{Tracer: tracer}
	noop := func(context.Context) error { return nil }

	require.NoError(t, new(wireBase(textStore, &deps)).Do(t.Context(), textPut, noop))
	require.NoError(
		t,
		new(wireBase("bus", &deps, client.Producer())).Do(t.Context(), textOrders, noop),
	)
	require.NoError(
		t,
		new(wireBase(
			"bus.handler",
			&deps,
			client.Handler(),
		)).Do(t.Context(), textOrders, noop),
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

	base := wireBase("stream", &decorate.Deps{Timeout: 20 * time.Millisecond})
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

// wireBase is the client Base named name whose every operation runs on deps, for tests
// that drive a client's chain with operation names of their own.
func wireBase(name string, deps *decorate.Deps, opts ...client.Option) client.Base {
	return fixtures.Must(client.NewWireBase(decorate.Fixed(deps), name, opts...))
}

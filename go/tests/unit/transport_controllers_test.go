package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport"
)

// echo is a controller handler that echoes its request.
func echo(_ context.Context, req string) (string, error) { return "echo-" + req, nil }

// TestControllerTier_OrdersObservabilityOutermost tests the controller decoration tier.
//
// Why this test is important:
//   - A request rejected by the rate limit, the bulkhead or auth, or one that panics,
//     must still get its server span, metrics and log line; and load must be rejected
//     before auth or the handler does any work.
//
// What it tests:
//   - The tier is named "controller", is the edge (logs failures above Debug), traces
//     with server spans, and orders tracing →
//     metrics → logging → recovery → rate limit → bulkhead → auth → timeout.
func TestControllerTier_OrdersObservabilityOutermost(t *testing.T) {
	t.Parallel()
	assert.Equal(
		t,
		decorate.Tier{
			Name:     "controller",
			SpanKind: interfaces.SpanKindServer,
			Edge:     true,
			Order: []decorate.Layer{
				decorate.LayerTracing,
				decorate.LayerMetrics,
				decorate.LayerLogging,
				decorate.LayerRecovery,
				decorate.LayerRateLimit,
				decorate.LayerBulkhead,
				decorate.LayerAuth,
				decorate.LayerTimeout,
			},
		},
		transport.Tier(),
	)
}

// TestControllerBase_HandlerRunsThroughTheTier tests Base.Handler.
//
// Why this test is important:
//   - Every REST and RPC handler is wrapped by Handler; it must return the handler's
//     result and error unchanged, and the zero Base must run handlers undecorated.
//
// What it tests:
//   - A decorated and an undecorated handler return the inner result.
//   - A handler error reaches the caller with its code.
//   - Auth sees the action "<controller>.<op>".
func TestControllerBase_HandlerRunsThroughTheTier(t *testing.T) {
	t.Parallel()

	var actions []string

	base := fixtures.Must(
		transport.NewBase[usersController](
			decorate.Fixed(fixtures.BuildRef(func(row *decorate.Deps) {
				row.Logger = fixtures.NopLogger()
				row.Metrics = fixtures.NopMetrics()
				row.Tracer = fixtures.NopTracer()
				row.Auth = func(_ context.Context, action string) error {
					actions = append(actions, action)

					return nil
				}
			})),
			textUsers,
		),
	)

	got, err := base.Handler(textGet, echo)(t.Context(), textA)
	require.NoError(t, err)
	assert.Equal(t, "echo-a", got)
	assert.Equal(t, []string{"users.get"}, actions)

	got, err = new(transport.Base).Handler(textGet, echo)(t.Context(), textB)
	require.NoError(t, err)
	assert.Equal(t, "echo-b", got)

	failing := func(context.Context, string) (string, error) {
		return "", apperr.New(apperr.CodeNotFound, "no such user")
	}

	_, err = base.Handler(textGet, failing)(t.Context(), textC)
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
}

// TestControllerBase_RejectsBeforeTheHandler tests the controller tier's guards.
//
// Why this test is important:
//   - A server under load must shed excess requests at once, never queue them, and a
//     panicking handler must not crash the process.
//
// What it tests:
//   - A denied rate limit returns CodeResourceExhausted without running the handler.
//   - A full bulkhead with ShedLoad returns CodeResourceExhausted at once.
//   - A panic becomes CodeInternal.
//   - A handler that outlives the timeout returns CodeTimeout.
func TestControllerBase_RejectsBeforeTheHandler(t *testing.T) {
	t.Parallel()

	ran := false
	handler := func(context.Context, string) (string, error) {
		ran = true

		return "", nil
	}

	limited := fixtures.Must(
		transport.NewBase[opController](
			decorate.Fixed(fixtures.BuildRef(func(row *decorate.Deps) {
				row.RateLimiter = fixtures.DenyingRateLimiter()
			})),
			textC,
		),
	)
	_, err := limited.Handler(textOp, handler)(t.Context(), "")
	assert.Equal(t, apperr.CodeResourceExhausted, apperr.Code(err))
	assert.False(t, ran)

	currentBulkhead, err := bulkhead.New(
		bulkhead.KindChannel,
		bulkhead.WithMaxConcurrent(1),
	)
	require.NoError(t, err)

	held, release := make(chan struct{}), make(chan struct{})

	go func() {
		_ = currentBulkhead.Execute(context.Background(), func() error {
			close(held)
			<-release

			return nil
		})
	}()

	<-held

	defer close(release)

	shedding := fixtures.Must(
		transport.NewBase[opController](
			decorate.Fixed(
				fixtures.BuildRef(func(row *decorate.Deps) {
					row.Bulkhead = currentBulkhead
					row.ShedLoad = true
				}),
			),
			textC,
		),
	)

	_, err = shedding.Handler(textOp, handler)(t.Context(), "")
	assert.Equal(t, apperr.CodeResourceExhausted, apperr.Code(err))
	assert.False(t, ran)

	testControllerBaseRejectsBeforeTheHandlerInternalAndTimeout(t)
}

// testControllerBaseRejectsBeforeTheHandlerInternalAndTimeout continues
// TestControllerBase_RejectsBeforeTheHandler: internal and timeout.
func testControllerBaseRejectsBeforeTheHandlerInternalAndTimeout(t *testing.T) {
	t.Helper()

	var err error

	panicking := func(context.Context, string) (string, error) { panic(textBoom) }

	_, err = new(
		fixtures.Must(transport.NewBase[opController](fixtures.Decorations(), textC)),
	).
		Handler(textOp, panicking)(
		t.Context(),
		"",
	)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))

	slow := func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()

		return "", ctx.Err()
	}
	bounded := fixtures.Must(
		transport.NewBase[opController](
			decorate.Fixed(
				fixtures.BuildRef(func(row *decorate.Deps) {
					row.Timeout = 10 * time.Millisecond
				}),
			),
			textC,
		),
	)

	_, err = bounded.Handler(textOp, slow)(t.Context(), "")
	assert.Equal(t, apperr.CodeTimeout, apperr.Code(err))
}

// TestDecorations_ControllerTierFromConfig tests the controllers tier of the
// decoration config.
//
// Why this test is important:
//   - The rate limit and load shedding must be switchable per controller by config
//     alone, and stay off by default so today's handlers keep their behavior.
//
// What it tests:
//   - By default a controller gets recovery and observability but no rate limiter
//     and no bulkhead.
//   - An override turns on the rate limit and a shedding bulkhead for one controller.
func TestDecorations_ControllerTierFromConfig(t *testing.T) {
	t.Parallel()

	cfg := decoration.DefaultConfig()
	enabled := true

	cfg.Controllers.Overrides = map[string]decoration.Layers{
		"upload": fixtures.Build(func(row *decoration.Layers) {
			row.RateLimit = decoration.RateLimit{
				Enabled:         &enabled,
				RateLimitConfig: stdx.Zero[resilience.RateLimitConfig](),
			}
			row.Bulkhead = decoration.Bulkhead{
				Enabled:        &enabled,
				Shed:           &enabled,
				BulkheadConfig: stdx.Zero[resilience.BulkheadConfig](),
			}
		}),
	}

	decs := decorate.NewDecorations(
		&cfg,
		fixtures.Build(func(row *decorate.Collaborators) {
			row.Logger = fixtures.NopLogger()
		}),
	)

	deps, err := decs.Deps(transport.Tier(), "search")
	require.NoError(t, err)
	assert.Nil(t, deps.RateLimiter)
	assert.Nil(t, deps.Bulkhead)
	assert.False(t, deps.SkipRecovery)
	assert.NotNil(t, deps.Logger)

	deps, err = decs.Deps(transport.Tier(), "upload")
	require.NoError(t, err)
	assert.NotNil(t, deps.RateLimiter)
	assert.NotNil(t, deps.Bulkhead)
	assert.True(t, deps.ShedLoad)
}

// usersController is the test controller's contract: one operation, get.
type usersController interface {
	// Get handles a get.
	Get(ctx context.Context, id string) (string, error)
}

// opController is a one-operation controller contract.
type opController interface {
	// Op handles the request.
	Op(ctx context.Context, in string) (string, error)
}

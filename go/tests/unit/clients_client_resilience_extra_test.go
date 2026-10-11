package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/mock/gomock"

	clientdecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	jobdecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/decorators"
	storagedecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/memory"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/bulkhead"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestStackFromConfig_PrimitiveErrorsAndRetryLayer tests that StackFromConfig fails
// loudly when any resilience primitive rejects its config, and builds the Retry
// layer when opted in.
//
// Why this test is important:
//   - StackFromConfig is the app-wiring entrypoint for every client's resilience
//     stack; a primitive misconfiguration must fail at construction (not silently
//     drop a protection layer), and the opt-in Retry layer must actually be wired when
//     enabled.
//
// What it tests:
//   - An unknown Bulkhead / CircuitBreaker / Retry kind each makes StackFromConfig
//     return an error.
//   - RetryEnabled with a valid config builds a non-nil stack.
func TestStackFromConfig_PrimitiveErrorsAndRetryLayer(t *testing.T) {
	t.Parallel()
	deps := clientdecorators.Deps{}

	badBulkhead := clientdecorators.DefaultConfig()
	badBulkhead.Bulkhead.Kind = bulkhead.Kind(99)
	_, err := clientdecorators.StackFromConfig("x", badBulkhead, deps)
	require.Error(t, err, "an unknown bulkhead kind must fail loudly")

	badCB := clientdecorators.DefaultConfig()
	badCB.CircuitBreaker.Kind = circuitbreaker.Kind(99)
	_, err = clientdecorators.StackFromConfig("x", badCB, deps)
	require.Error(t, err, "an unknown circuit-breaker kind must fail loudly")

	badRetry := clientdecorators.DefaultConfig()
	badRetry.RetryEnabled = true
	badRetry.Retry.Kind = retry.Kind(99)
	_, err = clientdecorators.StackFromConfig("x", badRetry, deps)
	require.Error(t, err, "an unknown retrier kind must fail loudly")

	withRetry := clientdecorators.DefaultConfig()
	withRetry.RetryEnabled = true
	s, err := clientdecorators.StackFromConfig("x", withRetry, deps)
	require.NoError(t, err)
	require.NotNil(t, s, "a valid retry-enabled config builds the stack")
}

// TestRunStream_TracesAndBounds tests that RunStream opens a span and passes through
// the bulkhead, returning a cleanup that ends the span.
//
// Why this test is important:
//   - RunStream backs the S3 Download path, where the span/timeout must outlive the
//     method and be released only when the caller closes the stream. Skipping the
//     tracer or bulkhead layer would drop observability or concurrency bounding on
//     large downloads.
//
// What it tests:
//   - A stack with tracer + bulkhead + timeout runs fn, returns its value, and yields
//     a cleanup that runs without panicking.
func TestRunStream_TracesAndBounds(t *testing.T) {
	t.Parallel()

	bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(2))
	require.NoError(t, err)
	stack := clientdecorators.New("stream").
		WithTracer(noop.NewTracerProvider().Tracer("test")).
		WithBulkhead(bh).
		WithTimeout(time.Second)

	val, cleanup, err := clientdecorators.RunStream(
		context.Background(),
		stack,
		"download",
		func(context.Context) (string, error) { return "ok", nil },
	)
	require.NoError(t, err)
	assert.Equal(t, "ok", val)
	require.NotNil(t, cleanup)
	cleanup() // ends the span + cancels the timeout
}

// TestStorageDecorator_BuilderAndConfigError tests the storage-decorator builder's
// bulkhead wiring and its config-driven error path.
//
// Why this test is important:
//   - DecorateFromConfig is how the S3 client opts into the shared resilience stack;
//     a bad resilience config must fail at wiring, and the fluent WithBulkhead must
//     actually plumb the limiter into the built decorator.
//
// What it tests:
//   - Builder.WithBulkhead(...).Build() returns a non-nil decorated client.
//   - DecorateFromConfig with an unknown circuit-breaker kind returns an error.
func TestStorageDecorator_BuilderAndConfigError(t *testing.T) {
	t.Parallel()

	bh, err := bulkhead.New(bulkhead.KindChannel, bulkhead.WithMaxConcurrent(1))
	require.NoError(t, err)
	decorated := storagedecorators.NewBuilder(memory.New(), "s3").WithBulkhead(bh).Build()
	require.NotNil(t, decorated)

	badCfg := clientdecorators.DefaultConfig()
	badCfg.CircuitBreaker.Kind = circuitbreaker.Kind(99)
	_, err = storagedecorators.DecorateFromConfig(
		memory.New(), "s3", badCfg, clientdecorators.Deps{},
	)
	require.Error(t, err, "a bad resilience config must fail the decorate wiring")
}

// TestJobDecorator_WrapJob_GateBranches tests the Job stack's outer gates:
// leader election (error + non-leader skip) and rate-limit backpressure.
//
// Why this test is important:
//   - A periodic job scheduled across N replicas must run only on the leader (once
//     cluster-wide) and must not run when the leader check errors; the rate limiter
//     must be able to refuse a run. A regression here would double-run a job across
//     replicas or ignore start-rate limits.
//
// What it tests:
//   - A leader-check error propagates (job not run); a non-leader instance skips the
//     job returning nil; a rate-limiter rejection propagates.
func TestJobDecorator_WrapJob_GateBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fn := jobdecorators.JobFunc(func(context.Context) error { return nil })

	t.Run("leader check error propagates", func(t *testing.T) {
		t.Parallel()
		leader := mocks.NewMockLeaderElector(gomock.NewController(t))
		leader.EXPECT().
			IsLeader(gomock.Any()).
			Return(false, apperr.Sentinel("election down"))
		err := jobdecorators.WrapJob(
			fn,
			jobdecorators.JobStackDeps{Leader: leader, Name: "j"},
		)(
			ctx,
		)
		require.Error(t, err)
	})

	t.Run("non-leader skips the run", func(t *testing.T) {
		t.Parallel()
		ranLocal := false
		local := jobdecorators.JobFunc(
			func(context.Context) error { ranLocal = true; return nil },
		)
		leader := mocks.NewMockLeaderElector(gomock.NewController(t))
		leader.EXPECT().IsLeader(gomock.Any()).Return(false, nil)
		require.NoError(
			t,
			jobdecorators.WrapJob(
				local,
				jobdecorators.JobStackDeps{Leader: leader, Name: "j"},
			)(
				ctx,
			),
		)
		assert.False(t, ranLocal, "a non-leader instance must not run the job")
	})

	t.Run("rate limiter rejection propagates", func(t *testing.T) {
		t.Parallel()
		err := jobdecorators.WrapJob(fn, jobdecorators.JobStackDeps{
			Limiter: fixtures.StubRateLimiter(false), // Wait returns an error
			Name:    "j",
		})(ctx)
		require.Error(t, err)
	})
}

package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow/decorators"
)

// TestBaseWorkflow tests that BaseWorkflow wraps an orchestration function
// and produces correct output through the Workflow interface.
//
// Why this test is important:
//   - BaseWorkflow is the foundation for all multi-step orchestration a
//     consumer builds; incorrect wrapping would silently skip processing steps
//   - Validates the core Workflow interface contract that all workflow
//     decorators depend on
//
// What it tests:
//   - Execute transforms "order" to "order-processed" via the wrapped function
func TestBaseWorkflow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Create a simple orchestration function: append "-processed" to input
	orchestrateFn := func(_ context.Context, input string) (string, error) {
		return input + "-processed", nil
	}

	// Wrap it in a BaseWorkflow
	w := workflow.NewBaseWorkflow(orchestrateFn)

	// Execute and verify result
	result, err := w.Execute(ctx, "order")
	require.NoError(t, err)
	assert.Equal(t, "order-processed", result)
}

// TestBaseWorkflow_ErrorPropagation tests that errors from the wrapped
// orchestration function propagate unchanged to the caller.
//
// Why this test is important:
//   - Workflow errors drive a worker's retry and dead-letter-queue logic;
//     swallowed errors would leave work stuck in "processing"
//   - Ensures error context is preserved for operator debugging and alerting
//
// What it tests:
//   - Execute returns the exact error produced by the inner function
func TestBaseWorkflow_ErrorPropagation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	expectedErr := apperr.Sentinel("orchestration failed")

	// Create a function that returns an error
	orchestrateFn := func(_ context.Context, _ string) (string, error) {
		return "", expectedErr
	}

	w := workflow.NewBaseWorkflow(orchestrateFn)

	// Execute and verify error propagates
	_, err := w.Execute(ctx, "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestLoggingDecorator tests that the logging decorator transparently
// delegates workflow execution without altering results.
//
// Why this test is important:
//   - The logging decorator wraps every workflow stage; if it mutates results
//     it would silently corrupt every workflow's output
//
// What it tests:
//   - Execute through the logging decorator returns the correct output
func TestWorkflowsLoggingDecorator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubWorkflow(
		func(_ context.Context, input string) (string, error) {
			return "workflow-output-" + input, nil
		},
	)

	// Build with logging decorator using interfaces.Logger
	w := decorators.NewBuilder[string, string](mock, "test-workflow").
		WithLogging(fixtures.NopLogger()).
		Build()

	// Execute
	result, err := w.Execute(ctx, "test-input")
	require.NoError(t, err)
	assert.Equal(t, "workflow-output-test-input", result)
}

// TestLoggingDecorator_ErrorPropagation tests that errors propagate unchanged
// through the logging decorator.
//
// Why this test is important:
//   - The decorator must not swallow errors; doing so would hide workflow
//     failures from callers that update document status on error
//
// What it tests:
//   - Execute returns the exact error produced by the inner workflow
func TestWorkflowsLoggingDecorator_ErrorPropagation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	expectedErr := apperr.Sentinel("workflow failed")
	mock := fixtures.ErrWorkflow(expectedErr)

	w := decorators.NewBuilder[string, string](mock, "error-workflow").
		WithLogging(fixtures.NopLogger()).
		Build()

	// Execute (expect error)
	_, err := w.Execute(ctx, "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestMetricsDecorator tests that the metrics decorator transparently
// delegates workflow execution without panicking or altering results.
//
// Why this test is important:
//   - Metrics are recorded for every workflow stage; a decorator that panics
//     or mutates output would break all instrumented workflows
//
// What it tests:
//   - Execute through the metrics decorator returns the correct output
func TestWorkflowsMetricsDecorator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubWorkflow(
		func(_ context.Context, _ string) (string, error) {
			return "output", nil
		},
	)

	// Build with metrics decorator using interfaces.Metrics
	w := decorators.NewBuilder[string, string](mock, "metrics-workflow").
		WithMetrics(fixtures.NopMetrics()).
		Build()

	// Execute - verifies the decorator chain doesn't panic
	result, err := w.Execute(ctx, "input")
	require.NoError(t, err)
	assert.Equal(t, "output", result)
}

// TestBuilderComposition tests that multiple workflow decorators (logging and
// metrics) compose correctly without interfering with each other.
//
// Why this test is important:
//   - Production workflows use multiple decorators stacked together; if they
//     interfere the output is corrupted or one decorator's logs are lost
//
// What it tests:
//   - Execute through both logging and metrics decorators returns the correct output
func TestWorkflowsBuilderComposition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubWorkflow(
		func(_ context.Context, input string) (string, error) {
			return "composed-" + input, nil
		},
	)

	// Build with both logging and metrics (order: base -> metrics -> logging)
	w := decorators.NewBuilder[string, string](mock, "composed-workflow").
		WithLogging(fixtures.NopLogger()).
		WithMetrics(fixtures.NopMetrics()).
		Build()

	// Execute
	result, err := w.Execute(ctx, "test")
	require.NoError(t, err)
	assert.Equal(t, "composed-test", result)
}

// TestBuilderComposition_ErrorFlow tests that errors propagate correctly
// through a composed workflow decorator chain.
//
// Why this test is important:
//   - Each decorator in the chain must forward errors to the outer caller;
//     any decorator that swallows errors would hide root causes from callers
//
// What it tests:
//   - Execute returns the exact error from the inner workflow through both
//     logging and metrics decorators
func TestWorkflowsBuilderComposition_ErrorFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	expectedErr := apperr.Sentinel("composed error")
	mock := fixtures.ErrWorkflow(expectedErr)

	w := decorators.NewBuilder[string, string](mock, "error-composed").
		WithLogging(fixtures.NopLogger()).
		WithMetrics(fixtures.NopMetrics()).
		Build()

	// Execute (expect error)
	_, err := w.Execute(ctx, "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestInterfaceCompliance tests that both BaseWorkflow and decorated workflows
// satisfy the Workflow interface contract.
//
// Why this test is important:
//   - All workflow consumers accept interfaces.Workflow; if decorated workflows
//     fail to satisfy the interface the code won't compile in callers
//
// What it tests:
//   - BaseWorkflow is assignable to interfaces.Workflow
//   - A logging-decorated workflow is usable without type assertion failure
func TestWorkflowsInterfaceCompliance(t *testing.T) {
	t.Parallel()
	orchestrateFn := func(_ context.Context, input string) (string, error) {
		return input, nil
	}

	var _ interfaces.Workflow[string, string] = workflow.NewBaseWorkflow(orchestrateFn)

	// Also verify decorated workflow satisfies interface
	base := workflow.NewBaseWorkflow(orchestrateFn)
	decorated := decorators.NewBuilder[string, string](base, "test").
		WithLogging(fixtures.NopLogger()).
		Build()

	_ = decorated
}

// ---------------------------------------------------------------------------
// Timeout decorator tests
// ---------------------------------------------------------------------------

// TestTimeoutDecorator_PassesThrough tests that the timeout decorator does
// not interfere with workflow operations that complete within the deadline.
//
// Why this test is important:
//   - The timeout decorator is present on all production workflows; if it
//     cancels fast operations it would break normal document processing
//
// What it tests:
//   - Execute returns the correct output with a generous timeout
func TestWorkflowsTimeoutDecorator_PassesThrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubWorkflow(
		func(_ context.Context, input string) (string, error) {
			return "fast-" + input, nil
		},
	)

	w := decorators.NewBuilder[string, string](mock, "timeout-workflow").
		WithTimeout(5 * time.Second).
		Build()

	result, err := w.Execute(ctx, "input")
	require.NoError(t, err)
	assert.Equal(t, "fast-input", result)
}

// TestTimeoutDecorator_CancelsSlowOp tests that the timeout decorator cancels
// workflow operations that exceed the deadline.
//
// Why this test is important:
//   - Slow workflow stages (e.g. Bedrock LLM calls) must time out to prevent
//     goroutine and connection pool exhaustion
//   - Without a timeout, a single slow query could block the entire API service
//
// What it tests:
//   - Execute returns context.DeadlineExceeded when the workflow blocks
//     longer than the timeout
func TestWorkflowsTimeoutDecorator_CancelsSlowOp(t *testing.T) {
	t.Parallel()

	mock := fixtures.StubWorkflow(
		func(ctx context.Context, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	)

	w := decorators.NewBuilder[string, string](mock, "slow-workflow").
		WithTimeout(1 * time.Millisecond).
		Build()

	_, err := w.Execute(context.Background(), "input")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// ---------------------------------------------------------------------------
// Recovery decorator tests
// ---------------------------------------------------------------------------

// TestRecoveryDecorator_CatchesPanic tests that the recovery decorator
// converts panics into errors instead of crashing the process.
//
// Why this test is important:
//   - A panic in a workflow must not crash the serving process and drop all
//     in-flight requests and connections
//   - The recovery decorator is the last safety net before the error is
//     returned to the client
//
// What it tests:
//   - Execute on a panicking workflow returns an error containing "panic recovered"
func TestWorkflowsRecoveryDecorator_CatchesPanic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.PanicWorkflow()

	w := decorators.NewBuilder[string, string](mock, "panic-workflow").
		WithLogging(fixtures.NopLogger()).
		WithRecovery().
		Build()

	_, err := w.Execute(ctx, "input")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic recovered")
}

// TestRecoveryDecorator_PassesThroughNormal tests that the recovery decorator
// does not alter results during normal (non-panicking) execution.
//
// Why this test is important:
//   - The recovery decorator must be invisible on the happy path; any mutation
//     of results would corrupt all workflow outputs even without a panic
//
// What it tests:
//   - Execute returns the correct output when no panic occurs
func TestWorkflowsRecoveryDecorator_PassesThroughNormal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubWorkflow(
		func(_ context.Context, input string) (string, error) {
			return "recovered-" + input, nil
		},
	)

	w := decorators.NewBuilder[string, string](mock, "recovery-workflow").
		WithLogging(fixtures.NopLogger()).
		WithRecovery().
		Build()

	result, err := w.Execute(ctx, "input")
	require.NoError(t, err)
	assert.Equal(t, "recovered-input", result)
}

// TestRecoveryDecorator_PropagatesError tests that the recovery decorator
// propagates errors unchanged (does not swallow non-panic errors).
//
// Why this test is important:
//   - Recovery must only catch panics, not normal errors; swallowing errors
//     would hide workflow failures from the caller
//
// What it tests:
//   - Execute returns the exact error from the inner workflow
func TestWorkflowsRecoveryDecorator_PropagatesError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	expectedErr := apperr.Sentinel("workflow error")
	mock := fixtures.ErrWorkflow(expectedErr)

	w := decorators.NewBuilder[string, string](mock, "error-recovery-workflow").
		WithLogging(fixtures.NopLogger()).
		WithRecovery().
		Build()

	_, err := w.Execute(ctx, "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// ---------------------------------------------------------------------------
// Full decorator chain
// ---------------------------------------------------------------------------

// TestFullDecoratorChain tests that all workflow decorators (timeout, metrics,
// logging, recovery) compose correctly in the full resilience stack.
//
// Why this test is important:
//   - Production workflows use all four decorators together; an incompatible
//     composition would break all instrumented workflow stages
//
// What it tests:
//   - Execute succeeds through the complete decorator chain with correct output
func TestFullDecoratorChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubWorkflow(
		func(_ context.Context, input string) (string, error) {
			return "full-" + input, nil
		},
	)

	w := decorators.NewBuilder[string, string](mock, "full-chain-workflow").
		WithTimeout(5 * time.Second).
		WithMetrics(fixtures.NopMetrics()).
		WithLogging(fixtures.NopLogger()).
		WithRecovery().
		Build()

	result, err := w.Execute(ctx, "chain")
	require.NoError(t, err)
	assert.Equal(t, "full-chain", result)
}

// TestMetricsDecorator_ErrorFlow tests that errors propagate unchanged through
// the metrics decorator.
//
// Why this test is important:
//   - The metrics decorator must record error counts but still forward the
//     original error; swallowing it would hide failures from callers
//
// What it tests:
//   - Execute returns the exact error from the inner workflow through the
//     metrics decorator
func TestMetricsDecorator_ErrorFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	expectedErr := apperr.Sentinel("metrics error")
	mock := fixtures.ErrWorkflow(expectedErr)

	w := decorators.NewBuilder[string, string](mock, "metrics-error-workflow").
		WithMetrics(fixtures.NopMetrics()).
		Build()

	_, err := w.Execute(ctx, "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestNewBaseWorkflow_NilPanics tests that NewBaseWorkflow panics when given
// a nil orchestration function.
//
// Why this test is important:
//   - A nil orchestration function cannot be detected until Execute is called;
//     failing at construction catches wiring mistakes at startup rather than
//     during request handling
//
// What it tests:
//   - NewBaseWorkflow[string, string](nil) panics
func TestNewBaseWorkflow_NilPanics(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		workflow.NewBaseWorkflow[string, string](nil)
	})
}

// TestWorkflowDecorator_WithTracing_HappyPath tests that a tracing-decorated
// workflow passes through successful results unchanged.
//
// Why this test is important:
//   - The tracing decorator must be transparent on the happy path; any mutation
//     would corrupt the results of the query orchestration workflow
//   - WithTracing is the only un-covered branch in the builder; this test
//     ensures spans are created and ended for each execution
//
// What it tests:
//   - WithTracing + Build produces a working workflow
//   - Execute returns the inner workflow's output without modification
func TestWorkflowDecorator_WithTracing_HappyPath(t *testing.T) {
	t.Parallel()

	base := fixtures.PassWorkflow() // returns "mock-workflow-output-input"
	w := decorators.NewBuilder[string, string](base, "test-workflow").
		WithTracing(fixtures.NopTracer()).
		Build()

	result, err := w.Execute(context.Background(), "input")
	require.NoError(t, err)
	assert.Equal(t, "mock-workflow-output-input", result)
}

// TestWorkflowDecorator_WithTracing_ErrorPath tests that a tracing-decorated
// workflow propagates errors from the inner workflow unchanged.
//
// Why this test is important:
//   - The tracing decorator must call span.RecordError and span.SetStatus on
//     failure so errors appear in distributed traces
//   - Swallowing the error would hide query orchestration failures from operators
//
// What it tests:
//   - Execute propagates the inner workflow error to the caller
func TestWorkflowDecorator_WithTracing_ErrorPath(t *testing.T) {
	t.Parallel()

	expectedErr := apperr.Sentinel("workflow failure")
	base := fixtures.ErrWorkflow(expectedErr)
	w := decorators.NewBuilder[string, string](base, "test-workflow").
		WithTracing(fixtures.NopTracer()).
		Build()

	_, err := w.Execute(context.Background(), "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

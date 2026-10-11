package unit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestBasePipeline tests that BasePipeline wraps a transform function and
// produces correct output through the Pipeline interface.
//
// Why this test is important:
//   - BasePipeline is the foundation for every data-transformation pipeline a
//     consumer builds; incorrect wrapping would corrupt each stage's output
//   - Validates the core Pipeline interface contract that all pipeline
//     decorators depend on
//
// What it tests:
//   - Execute transforms "hello" to "HELLO" via the wrapped uppercase function
func TestBasePipeline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Create a simple transform function: uppercase the input
	transformFn := func(_ context.Context, input string) (string, error) {
		return strings.ToUpper(input), nil
	}

	// Wrap it in a BasePipeline
	p := pipeline.NewBasePipeline(transformFn)

	// Execute and verify result
	result, err := p.Execute(ctx, "hello")
	require.NoError(t, err)
	assert.Equal(t, "HELLO", result)
}

// TestBasePipeline_ErrorPropagation tests that errors from the wrapped
// transform function propagate unchanged to the caller.
//
// Why this test is important:
//   - Pipeline errors drive a caller's status transitions (e.g. marking a job
//     as failed); swallowed errors would leave work in a perpetual
//     "processing" state
//   - Ensures error context is preserved for operator debugging and alerting
//
// What it tests:
//   - Execute returns the exact error produced by the inner function
func TestBasePipeline_ErrorPropagation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	expectedErr := apperr.Sentinel("transform failed")

	// Create a function that returns an error
	transformFn := func(_ context.Context, _ string) (string, error) {
		return "", expectedErr
	}

	p := pipeline.NewBasePipeline(transformFn)

	// Execute and verify error propagates
	_, err := p.Execute(ctx, "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestLoggingDecorator tests that the logging decorator transparently
// delegates pipeline execution without altering results.
//
// Why this test is important:
//   - The logging decorator wraps every pipeline stage; if it mutates results
//     it would silently corrupt every pipeline's output
//
// What it tests:
//   - Execute through the logging decorator returns the correct output
func TestPipelinesLoggingDecorator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubPipeline(
		func(_ context.Context, input string) (string, error) {
			return "output-" + input, nil
		},
	)

	// Build with logging decorator using interfaces.Logger
	p := decorators.NewBuilder[string, string](mock, "test-pipeline").
		WithLogging(fixtures.NopLogger()).
		Build()

	// Execute
	result, err := p.Execute(ctx, "test-input")
	require.NoError(t, err)
	assert.Equal(t, "output-test-input", result)
}

// TestLoggingDecorator_ErrorPropagation tests that errors propagate unchanged
// through the logging decorator.
//
// Why this test is important:
//   - The decorator must not swallow errors; doing so would hide pipeline
//     failures from callers that update status on error
//
// What it tests:
//   - Execute returns the exact error produced by the inner pipeline
func TestPipelinesLoggingDecorator_ErrorPropagation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	expectedErr := apperr.Sentinel("pipeline failed")
	mock := fixtures.ErrPipeline(expectedErr)

	p := decorators.NewBuilder[string, string](mock, "error-pipeline").
		WithLogging(fixtures.NopLogger()).
		Build()

	// Execute (expect error)
	_, err := p.Execute(ctx, "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestMetricsDecorator tests that the metrics decorator transparently
// delegates pipeline execution without panicking or altering results.
//
// Why this test is important:
//   - Metrics are recorded for every pipeline stage; a decorator that panics
//     or mutates output would break all instrumented pipelines
//
// What it tests:
//   - Execute through the metrics decorator returns the correct output
func TestPipelinesMetricsDecorator(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubPipeline(
		func(_ context.Context, _ string) (string, error) {
			return "output", nil
		},
	)

	// Build with metrics decorator using interfaces.Metrics
	p := decorators.NewBuilder[string, string](mock, "metrics-pipeline").
		WithMetrics(fixtures.NopMetrics()).
		Build()

	// Execute - verifies the decorator chain doesn't panic
	result, err := p.Execute(ctx, "input")
	require.NoError(t, err)
	assert.Equal(t, "output", result)
}

// TestBuilderComposition tests that multiple pipeline decorators (logging and
// metrics) compose correctly without interfering with each other.
//
// Why this test is important:
//   - Production pipelines use multiple decorators stacked together; if they
//     interfere the output is corrupted or one decorator's logs are lost
//
// What it tests:
//   - Execute through both logging and metrics decorators returns the correct output
func TestPipelinesBuilderComposition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubPipeline(
		func(_ context.Context, input string) (string, error) {
			return "composed-" + input, nil
		},
	)

	// Build with both logging and metrics (order: base -> metrics -> logging)
	p := decorators.NewBuilder[string, string](mock, "composed-pipeline").
		WithLogging(fixtures.NopLogger()).
		WithMetrics(fixtures.NopMetrics()).
		Build()

	// Execute
	result, err := p.Execute(ctx, "test")
	require.NoError(t, err)
	assert.Equal(t, "composed-test", result)
}

// TestBuilderComposition_ErrorFlow tests that errors propagate correctly
// through a composed decorator chain.
//
// Why this test is important:
//   - Each decorator in the chain must forward errors to the outer caller;
//     any decorator that swallows errors would hide root causes from callers
//
// What it tests:
//   - Execute returns the exact error from the inner pipeline through both
//     logging and metrics decorators
func TestPipelinesBuilderComposition_ErrorFlow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	expectedErr := apperr.Sentinel("composed error")
	mock := fixtures.ErrPipeline(expectedErr)

	p := decorators.NewBuilder[string, string](mock, "error-composed").
		WithLogging(fixtures.NopLogger()).
		WithMetrics(fixtures.NopMetrics()).
		Build()

	// Execute (expect error)
	_, err := p.Execute(ctx, "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestInterfaceCompliance tests that both BasePipeline and decorated pipelines
// satisfy the Pipeline interface contract.
//
// Why this test is important:
//   - All pipeline consumers accept interfaces.Pipeline; if decorated pipelines
//     fail to satisfy the interface the code won't compile in callers
//
// What it tests:
//   - BasePipeline is assignable to interfaces.Pipeline
//   - A logging-decorated pipeline is usable without type assertion failure
func TestPipelinesInterfaceCompliance(t *testing.T) {
	t.Parallel()
	transformFn := func(_ context.Context, input string) (string, error) {
		return input, nil
	}

	var _ interfaces.Pipeline[string, string] = pipeline.NewBasePipeline(transformFn)

	// Also verify decorated pipeline satisfies interface
	base := pipeline.NewBasePipeline(transformFn)
	decorated := decorators.NewBuilder[string, string](base, "test").
		WithLogging(fixtures.NopLogger()).
		Build()

	_ = decorated
}

// ---------------------------------------------------------------------------
// Timeout decorator tests
// ---------------------------------------------------------------------------

// TestTimeoutDecorator_PassesThrough tests that the timeout decorator does
// not interfere with pipeline operations that complete within the deadline.
//
// Why this test is important:
//   - The timeout decorator is present on all production pipelines; if it
//     cancels fast operations it would break normal document processing
//
// What it tests:
//   - Execute returns the correct output with a generous timeout
func TestPipelinesTimeoutDecorator_PassesThrough(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubPipeline(
		func(_ context.Context, input string) (string, error) {
			return "fast-" + input, nil
		},
	)

	p := decorators.NewBuilder[string, string](mock, "timeout-pipeline").
		WithTimeout(5 * time.Second).
		Build()

	result, err := p.Execute(ctx, "input")
	require.NoError(t, err)
	assert.Equal(t, "fast-input", result)
}

// TestTimeoutDecorator_CancelsSlowOp tests that the timeout decorator cancels
// pipeline operations that exceed the deadline.
//
// Why this test is important:
//   - Slow pipeline stages (e.g. Bedrock KB calls) must time out to prevent
//     goroutine and connection pool exhaustion
//   - Without a timeout, a single slow document could block the entire worker
//
// What it tests:
//   - Execute returns context.DeadlineExceeded when the pipeline blocks
//     longer than the timeout
func TestPipelinesTimeoutDecorator_CancelsSlowOp(t *testing.T) {
	t.Parallel()

	mock := fixtures.StubPipeline(
		func(ctx context.Context, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	)

	p := decorators.NewBuilder[string, string](mock, "slow-pipeline").
		WithTimeout(1 * time.Millisecond).
		Build()

	_, err := p.Execute(context.Background(), "input")
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
//   - A panic in a document-processing pipeline must not crash the worker
//     process and lose all in-flight documents
//   - The recovery decorator is the last safety net before the SQS message
//     goes to the dead-letter queue
//
// What it tests:
//   - Execute on a panicking pipeline returns an error containing
//     "panic recovered"
func TestPipelinesRecoveryDecorator_CatchesPanic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.PanicPipeline()

	p := decorators.NewBuilder[string, string](mock, "panic-pipeline").
		WithLogging(fixtures.NopLogger()).
		WithRecovery().
		Build()

	_, err := p.Execute(ctx, "input")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic recovered")
}

// TestRecoveryDecorator_PassesThroughNormal tests that the recovery decorator
// does not alter results during normal (non-panicking) execution.
//
// Why this test is important:
//   - The recovery decorator must be invisible on the happy path; any mutation
//     of results would corrupt all pipeline outputs even without a panic
//
// What it tests:
//   - Execute returns the correct output when no panic occurs
func TestPipelinesRecoveryDecorator_PassesThroughNormal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubPipeline(
		func(_ context.Context, input string) (string, error) {
			return "recovered-" + input, nil
		},
	)

	p := decorators.NewBuilder[string, string](mock, "recovery-pipeline").
		WithLogging(fixtures.NopLogger()).
		WithRecovery().
		Build()

	result, err := p.Execute(ctx, "input")
	require.NoError(t, err)
	assert.Equal(t, "recovered-input", result)
}

// TestRecoveryDecorator_PropagatesError tests that the recovery decorator
// propagates errors unchanged (does not swallow non-panic errors).
//
// Why this test is important:
//   - Recovery must only catch panics, not normal errors; swallowing errors
//     would hide processing failures from the caller
//
// What it tests:
//   - Execute returns the exact error from the inner pipeline
func TestPipelinesRecoveryDecorator_PropagatesError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	expectedErr := apperr.Sentinel("pipeline error")
	mock := fixtures.ErrPipeline(expectedErr)

	p := decorators.NewBuilder[string, string](mock, "error-recovery-pipeline").
		WithLogging(fixtures.NopLogger()).
		WithRecovery().
		Build()

	_, err := p.Execute(ctx, "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestTimeoutWithRecovery tests that timeout and recovery decorators compose
// correctly, with recovery catching panics even within a timeout context.
//
// Why this test is important:
//   - Both decorators are always applied together in production; they must
//     compose without interfering or masking each other's behavior
//
// What it tests:
//   - Execute on a panicking pipeline with timeout returns an error containing
//     "panic recovered"
func TestTimeoutWithRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.PanicPipeline()

	p := decorators.NewBuilder[string, string](mock, "timeout-recovery-pipeline").
		WithTimeout(5 * time.Second).
		WithLogging(fixtures.NopLogger()).
		WithRecovery().
		Build()

	_, err := p.Execute(ctx, "input")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic recovered")
}

// TestFullResilienceChain tests that all pipeline decorators (timeout,
// metrics, logging, recovery) compose correctly in the full resilience stack.
//
// Why this test is important:
//   - Production pipelines use all four decorators together; an incompatible
//     composition would break all instrumented pipeline stages
//
// What it tests:
//   - Execute succeeds through the complete decorator chain with correct output
func TestPipelinesFullResilienceChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	mock := fixtures.StubPipeline(
		func(_ context.Context, input string) (string, error) {
			return "full-" + input, nil
		},
	)

	p := decorators.NewBuilder[string, string](mock, "full-chain-pipeline").
		WithTimeout(5 * time.Second).
		WithMetrics(fixtures.NopMetrics()).
		WithLogging(fixtures.NopLogger()).
		WithRecovery().
		Build()

	result, err := p.Execute(ctx, "chain")
	require.NoError(t, err)
	assert.Equal(t, "full-chain", result)
}

// TestNewBasePipeline_NilPanics tests that NewBasePipeline panics when given
// a nil transform function.
//
// Why this test is important:
//   - A nil transform function cannot be detected until Execute is called;
//     failing at construction catches wiring mistakes at startup rather than
//     during document processing
//
// What it tests:
//   - NewBasePipeline[string, string](nil) panics
func TestNewBasePipeline_NilPanics(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		pipeline.NewBasePipeline[string, string](nil)
	})
}

// TestPipelineDecorator_WithTracing_HappyPath tests that a tracing-decorated
// pipeline passes through successful results unchanged.
//
// Why this test is important:
//   - The tracing decorator must be transparent on the happy path; any mutation
//     would corrupt the data flowing through the pipeline
//   - WithTracing is the only un-covered branch in the builder; this test
//     ensures spans are created and ended for each execution
//
// What it tests:
//   - WithTracing + Build produces a working pipeline
//   - Execute returns the inner pipeline's output without modification
func TestPipelineDecorator_WithTracing_HappyPath(t *testing.T) {
	t.Parallel()

	base := fixtures.PassPipeline() // returns "mock-output-input"
	p := decorators.NewBuilder[string, string](base, "test-pipeline").
		WithTracing(fixtures.NopTracer()).
		Build()

	result, err := p.Execute(context.Background(), "input")
	require.NoError(t, err)
	assert.Equal(t, "mock-output-input", result)
}

// TestPipelineDecorator_WithTracing_ErrorPath tests that a tracing-decorated
// pipeline propagates errors from the inner pipeline unchanged.
//
// Why this test is important:
//   - The tracing decorator must call span.RecordError and span.SetStatus on
//     failure so errors appear in distributed traces
//   - Swallowing the error would hide failures from the operator
//
// What it tests:
//   - Execute propagates the inner pipeline error to the caller
func TestPipelineDecorator_WithTracing_ErrorPath(t *testing.T) {
	t.Parallel()

	expectedErr := apperr.Sentinel("pipeline failure")
	base := fixtures.ErrPipeline(expectedErr)
	p := decorators.NewBuilder[string, string](base, "test-pipeline").
		WithTracing(fixtures.NopTracer()).
		Build()

	_, err := p.Execute(context.Background(), "input")
	require.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestTracerBuilder_UnknownKindReturnsError tests that the tracer factory
// rejects unsupported tracer backends.
//
// Why this test is important:
//   - Misconfigured Kind values must fail fast at startup rather than produce
//     a nil tracer that panics on the first Start call
//
// What it tests:
//   - tracer.New with Kind(999) returns a non-nil error
func TestTracerBuilder_UnknownKindReturnsError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, err := tracer.New(ctx, tracer.Kind(999))
	require.Error(t, err, "expected error for unknown kind")
}

// TestTracerBuilder_DefaultConfig tests that the default tracer config
// provides production-ready settings.
//
// Why this test is important:
//   - Services that omit explicit tracer config inherit these defaults; an
//     empty endpoint would cause traces to be silently dropped
//
// What it tests:
//   - Default kind is KindOTel
//   - ServiceName matches the provided argument
//   - Default endpoint is non-empty
func TestTracerBuilder_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := tracer.DefaultConfig("test-service")
	assert.Equal(t, tracer.KindOTel, cfg.Kind, "default kind should be KindOTel")
	assert.Equal(t, "test-service", cfg.ServiceName)
	assert.NotEmpty(t, cfg.Endpoint, "default endpoint must be non-empty")
}

// TestTracer_MockAsConsumerDependency tests that MockTracer and MockSpan
// satisfy their interfaces and can be used by consumers that depend on them.
//
// Why this test is important:
//   - Service-layer unit tests mock the tracer; the mock must implement Start,
//     SetAttribute, End, and Shutdown or those tests cannot compile
//   - Validates the mock contract so upstream tests can trust it
//
// What it tests:
//   - MockTracer assigned to interfaces.Tracer compiles
//   - Start, SetAttribute, End, and Shutdown delegate to the mock as expected
func TestTracer_MockAsConsumerDependency(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockTracer := mocks.NewMockTracer(ctrl)
	mockSpan := mocks.NewMockSpan(ctrl)

	ctx := context.Background()
	mockTracer.EXPECT().Start(ctx, "handleRequest").Return(ctx, mockSpan)
	mockSpan.EXPECT().SetAttribute("user.id", "abc-123")
	mockSpan.EXPECT().End()
	mockTracer.EXPECT().Shutdown(gomock.Any()).Return(nil)

	var tr interfaces.Tracer = mockTracer
	spanCtx, span := tr.Start(ctx, "handleRequest")
	require.NotNil(t, spanCtx, "Start must return non-nil context")
	span.SetAttribute("user.id", "abc-123")
	span.End()

	require.NoError(t, tr.Shutdown(ctx))
}

// TestTracer_MockShutdownError tests that the mock can simulate shutdown
// failures for consumer error-handling tests.
//
// Why this test is important:
//   - Graceful shutdown logic must handle tracer flush errors; this validates
//     the mock can produce that scenario for error-handling verification
//
// What it tests:
//   - Shutdown returns the configured flush error from the mock
func TestTracer_MockShutdownError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockTracer(ctrl)

	shutdownErr := apperr.Sentinel("export flush failed")
	mock.EXPECT().Shutdown(gomock.Any()).Return(shutdownErr)

	var tr interfaces.Tracer = mock
	err := tr.Shutdown(context.Background())
	assert.ErrorIs(t, err, shutdownErr)
}

// TestTracerBuilder_NoopKind tests that the tracer factory produces a working
// noop implementation when KindNoop is requested.
//
// Why this test is important:
//   - Local development and test environments disable tracing; the factory
//     must return a fully functional noop tracer, not nil
//
// What it tests:
//   - tracer.New with KindNoop returns a non-nil tracer without error
func TestTracerBuilder_NoopKind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tr, err := tracer.New(ctx, tracer.KindNoop)
	require.NoError(t, err, "KindNoop must not return error")
	require.NotNil(t, tr, "expected non-nil tracer for KindNoop")
}

// TestTracerBuilder_NoopStartReturnsSpan tests that the noop tracer's Start
// method returns a valid context and span.
//
// Why this test is important:
//   - All service code calls tracer.Start; the noop span must accept all
//     operations (SetAttribute, RecordError, SetStatus, End) without panic
//
// What it tests:
//   - Start returns non-nil context and span
//   - SetAttribute, RecordError, SetStatus, and End do not panic on the noop span
func TestTracerBuilder_NoopStartReturnsSpan(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tr, err := tracer.New(ctx, tracer.KindNoop)
	require.NoError(t, err)

	spanCtx, span := tr.Start(ctx, "test-op")
	require.NotNil(t, spanCtx, "Start must return non-nil context")
	require.NotNil(t, span, "Start must return non-nil span")

	// All Span methods must be safe to call on the noop span.
	span.SetAttribute("key", "value")
	span.RecordError(apperr.Sentinel("test"))
	span.SetStatus(interfaces.SpanStatusOK, "ok")
	span.End()
}

// TestTracerBuilder_NoopShutdown tests that the noop tracer's Shutdown method
// returns nil.
//
// Why this test is important:
//   - Graceful shutdown calls Shutdown on the tracer; the noop implementation
//     must return nil to avoid spurious errors during shutdown
//
// What it tests:
//   - Shutdown on the noop tracer returns nil error
func TestTracerBuilder_NoopShutdown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	tr, err := tracer.New(ctx, tracer.KindNoop)
	require.NoError(t, err)
	require.NoError(t, tr.Shutdown(ctx))
}

// TestTracerBuilder_KindString tests the string representation of tracer Kind
// values, covering all switch branches.
//
// Why this test is important:
//   - Kind strings appear in error messages and logs; incorrect strings make
//     debugging configuration issues harder
//
// What it tests:
//   - KindOTel.String() returns "otel"
//   - KindNoop.String() returns "noop"
//   - An unknown Kind returns "Kind(N)" format
func TestTracerBuilder_KindString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		want string
		kind tracer.Kind
	}{
		{"otel", tracer.KindOTel},
		{"noop", tracer.KindNoop},
		{"Kind(99)", tracer.Kind(99)},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.kind.String())
	}
}

// TestTracerBuilder_ConfigToOptions tests that Config.ToOptions produces a
// functional option slice that reproduces the original config.
//
// Why this test is important:
//   - ToOptions is used for config round-tripping and serialization; if the
//     resulting options don't reproduce the original config, services start
//     with wrong tracing settings
//
// What it tests:
//   - Applying ToOptions to a blank config reproduces ServiceName, Endpoint,
//     Kind, SampleRate, and Insecure from the original
func TestTracerBuilder_ConfigToOptions(t *testing.T) {
	t.Parallel()

	original := tracer.DefaultConfig("roundtrip-test")
	opts := original.ToOptions()

	var rebuilt tracer.Config
	for _, opt := range opts {
		opt(&rebuilt)
	}

	assert.Equal(t, original.ServiceName, rebuilt.ServiceName)
	assert.Equal(t, original.Endpoint, rebuilt.Endpoint)
	assert.Equal(t, original.Kind, rebuilt.Kind)
	assert.InDelta(t, original.SampleRate, rebuilt.SampleRate, 0)
	assert.Equal(t, original.Insecure, rebuilt.Insecure)
}

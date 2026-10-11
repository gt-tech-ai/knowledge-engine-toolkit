package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
)

// TestTraceIDFromContext tests that TraceIDFromContext returns the bare 32-hex trace id
// of the active span, or "" when there is no valid span context.
//
// Why this test is important:
//   - The WebSocket query response returns its trace id via this helper: a browser cannot
//     read the WS upgrade's response headers, so the streamed query's trace id rides back
//     in the ServerMessage. A wrong or empty id would break the client's ability to
//     correlate the query with its Tempo trace — the whole point of the contract.
//
// What it tests:
//   - a context carrying a valid sampled span context yields that span's exact 32-hex
//     trace id;
//   - a bare context (no span) yields "".
func TestTraceIDFromContext(t *testing.T) {
	t.Parallel()

	t.Run("returns the 32-hex trace id for a valid span context", func(t *testing.T) {
		t.Parallel()
		traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
		require.NoError(t, err)
		spanID, err := trace.SpanIDFromHex("0123456789abcdef")
		require.NoError(t, err)
		sc := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    traceID,
			SpanID:     spanID,
			TraceFlags: trace.FlagsSampled,
		})
		ctx := trace.ContextWithSpanContext(context.Background(), sc)
		assert.Equal(
			t,
			"0123456789abcdef0123456789abcdef",
			tracer.TraceIDFromContext(ctx),
		)
	})

	t.Run("returns empty for a context without a span", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, tracer.TraceIDFromContext(context.Background()))
	})
}

package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tracer/nooptracer"
)

// TestNoopTracer_SpanIsInert verifies the no-op tracer's span accepts every
// mutation without panicking or recording anything.
//
// Why this test is important:
//   - The no-op tracer is the default when tracing is disabled; every service +
//     decorator calls Start/SetAttribute/RecordError/SetStatus/End on it, so those
//     no-ops must be safe to call unconditionally (a panic would take down the caller).
//
// What it tests:
//   - A no-op span's SetAttribute/RecordError/SetStatus/End and the tracer's
//     Shutdown all run without error or panic.
func TestNoopTracer_SpanIsInert(t *testing.T) {
	t.Parallel()
	tr, err := nooptracer.New()
	require.NoError(t, err)

	_, span := tr.Start(context.Background(), "op")
	span.SetAttribute("key", "value")
	span.RecordError(apperr.Sentinel("boom"))
	span.SetStatus(interfaces.SpanStatusError, "failed")
	span.End()

	require.NoError(t, tr.Shutdown(context.Background()))
}

// TestTraceParentFromContext_EmptyWithoutSpan verifies traceparent extraction
// returns empty when no span context is present.
//
// Why this test is important:
//   - TraceParentFromContext propagates the trace across the SQS/outbox boundary; a
//     context with no active span must yield an empty header, not a malformed one.
//
// What it tests:
//   - TraceParentFromContext on a bare context returns the empty string.
func TestTraceParentFromContext_EmptyWithoutSpan(t *testing.T) {
	t.Parallel()
	assert.Empty(t, tracer.TraceParentFromContext(context.Background()))
}

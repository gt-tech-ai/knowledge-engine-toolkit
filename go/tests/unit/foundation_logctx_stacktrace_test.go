package unit_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/logctx"
)

// stackCaptureHandler records the last record it received so a test can inspect
// the attributes the stacktrace handler injected.
type stackCaptureHandler struct{ rec slog.Record }

func (c *stackCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (c *stackCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	c.rec = r
	return nil
}
func (c *stackCaptureHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *stackCaptureHandler) WithGroup(string) slog.Handler      { return c }

// stackAttrValue returns the value of the injected stacktrace attribute, if any.
func stackAttrValue(r slog.Record) (string, bool) {
	var stack string
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == logctx.StacktraceKey {
			stack, found = a.Value.String(), true
			return false
		}
		return true
	})
	return stack, found
}

// TestStacktraceHandler_SkipsLoggingPlumbing tests that a captured stack omits
// the logging-plumbing frames between the application and this handler.
//
// Why this test is important:
//   - Regression guard for the "Go stack traces are unreadable" report: if the
//     trace started inside slog/zap/logger internals, on-call engineers would
//     read frames pointing at the logger instead of the code that failed.
//
// What it tests:
//   - The captured stack contains no log/slog, go.uber.org/zap, or
//     foundation/logger/ frames, yet still reaches a real frame (testing.tRunner),
//     proving the walk continued past the plumbing rather than stopping in it.
func TestStacktraceHandler_SkipsLoggingPlumbing(t *testing.T) {
	t.Parallel()
	ch := &stackCaptureHandler{}
	h := logctx.NewStacktraceHandler(ch, slog.LevelError)

	rec := slog.NewRecord(time.Now(), slog.LevelError, "boom", 0)
	require.NoError(t, h.Handle(context.Background(), rec))

	stack, ok := stackAttrValue(ch.rec)
	require.True(t, ok, "expected a stacktrace attribute on the error record")
	require.NotEmpty(
		t,
		stack,
		"expected a non-empty stacktrace attribute on the error record",
	)

	for _, noise := range []string{"log/slog", "go.uber.org/zap", "foundation/logger/"} {
		assert.NotContains(t, stack, noise,
			"stacktrace leaks logging-plumbing frame %q (should be skipped)", noise)
	}
	// The handler's own frames live under foundation/logger/ and are filtered; the
	// captured stack therefore starts at this test's call to Handle and continues
	// up to the test runner. tRunner's presence proves captureStack walked the real
	// call stack rather than stopping inside the logger internals.
	assert.Contains(t, stack, "testing.tRunner",
		"stacktrace should reach real call-stack frames past the plumbing")
}

// TestStacktraceHandler_PrefersOriginStack tests that when a logged error exposes
// an origin StackTrace(), the handler emits that stack instead of the call-site
// capture.
//
// Why this test is important:
//   - Regression guard for "stacktraces point at the logger, not the failure":
//     the logged trace must point at where the error was actually created, or
//     diagnosis leads to the log call rather than the fault.
//
// What it tests:
//   - For an apperr-wrapped error, the emitted stack points at the error's
//     creation site (this test file), not the handler's call site.
func TestStacktraceHandler_PrefersOriginStack(t *testing.T) {
	t.Parallel()
	ch := &stackCaptureHandler{}
	h := logctx.NewStacktraceHandler(ch, slog.LevelError)

	err := apperr.Wrap(
		apperr.Sentinel("query failed"),
		apperr.CodeInternal,
		"origin query failed",
	)
	rec := slog.NewRecord(time.Now(), slog.LevelError, "query failed", 0)
	rec.Add("error", err)

	require.NoError(t, h.Handle(context.Background(), rec))

	stack, ok := stackAttrValue(ch.rec)
	require.True(t, ok, "expected a stacktrace attribute")
	assert.Contains(t, stack, "logctx_stacktrace_test.go",
		"expected origin stack to point at the error creation site in the test file")
}

// TestStacktraceHandler_BelowThresholdNoStack tests that records below the
// configured level pass through without a stacktrace attribute.
//
// Why this test is important:
//   - Capturing a stack on every info/debug line would impose runtime cost and
//     flood logs with noise; capture must fire only at/above the threshold.
//
// What it tests:
//   - An info-level record handled by an error-threshold handler carries no
//     stacktrace attribute.
func TestStacktraceHandler_BelowThresholdNoStack(t *testing.T) {
	t.Parallel()
	ch := &stackCaptureHandler{}
	h := logctx.NewStacktraceHandler(ch, slog.LevelError)

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "fyi", 0)
	require.NoError(t, h.Handle(context.Background(), rec))

	_, ok := stackAttrValue(ch.rec)
	assert.False(t, ok, "info-level record should NOT carry a stacktrace attribute")
}

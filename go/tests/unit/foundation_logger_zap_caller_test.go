package unit_test

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	zaplogger "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger/zap"
)

// zapCaptureStdout runs fn with os.Stdout redirected to a pipe and returns what
// was written. The zap logger binds to os.Stdout at construction, so callers must
// build the logger inside fn.
func zapCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// zapLastJSONLine parses the last non-empty JSON line from captured logger output.
func zapLastJSONLine(t *testing.T, s string) map[string]any {
	t.Helper()
	var rec map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		require.NoError(t, json.Unmarshal([]byte(line), &rec),
			"log line is not JSON: %q", line)
	}
	require.NotNil(t, rec, "no log output captured")
	return rec
}

// TestZapLogger_CallerIsCallSite tests that the logged "caller" field points at
// the code that called the logger, not this package's wrapper method.
//
// Why this test is important:
//   - Regression guard for the "caller is always zap/logger.go" report: if every
//     line attributes itself to the wrapper, the caller field is useless for
//     locating where a log statement actually lives.
//
// What it tests:
//   - An Info call's caller field excludes zap/logger.go and points at the
//     calling code (this test file).
func TestZapLogger_CallerIsCallSite(t *testing.T) {
	out := zapCaptureStdout(t, func() {
		zaplogger.New(zaplogger.Config{Level: "info", Format: "json", RedactPII: false}).
			Info("hello")
	})
	rec := zapLastJSONLine(t, out)

	caller, _ := rec["caller"].(string)
	require.NotEmpty(t, caller, "expected a caller field in the log output")
	assert.NotContains(t, caller, "zap/logger.go",
		"caller still points at the logger wrapper instead of the call site")
	assert.Contains(t, caller, "logger_zap_caller_test.go",
		"caller should point at the calling code (this test file)")
}

// TestZapLogger_AppErrorSurfacesCauseAndStack tests that logging an AppError emits
// a structured error object carrying the wrapped cause and an origin stack.
//
// Why this test is important:
//   - Regression guard for the "failed to query database" incident: without the
//     underlying cause and an origin stack in the log, the real failure (a
//     Postgres "column does not exist") is invisible and undiagnosable from logs.
//
// What it tests:
//   - The error object's cause surfaces the underlying DB message.
//   - The error object's stack and the top-level stacktrace are rooted at the
//     caller (this test file), not the logging call site (logger/zap/logger.go).
func TestZapLogger_AppErrorSurfacesCauseAndStack(t *testing.T) {
	out := zapCaptureStdout(t, func() {
		underlying := apperr.Sentinel(
			`ERROR: column "deleted_at" does not exist (SQLSTATE 42703)`,
		)
		err := apperr.Wrap(underlying, apperr.CodeInternal, "failed to query database")
		zaplogger.New(zaplogger.Config{Level: "info", Format: "json", RedactPII: false}).
			Error("pipeline.Execute failed", "error", err)
	})
	rec := zapLastJSONLine(t, out)

	errObj, ok := rec["error"].(map[string]any)
	require.Truef(
		t,
		ok,
		"expected structured error object, got %T: %v",
		rec["error"],
		rec["error"],
	)

	cause, _ := errObj["cause"].(string)
	assert.Contains(t, cause, "deleted_at",
		"expected cause to surface the underlying DB error")

	stack, _ := errObj["stack"].(string)
	assert.Contains(t, stack, "logger_zap_caller_test.go",
		"expected origin stack rooted at the caller")

	// The single top-level "stacktrace" must be the error's origin stack (rooted
	// at the caller that created it), not the logger's own call site.
	topStack, _ := rec["stacktrace"].(string)
	assert.Contains(t, topStack, "logger_zap_caller_test.go",
		"expected top-level stacktrace rooted at the caller")
	assert.NotContains(t, topStack, "logger/zap/logger.go",
		"top-level stacktrace still points at the logging call site")
}

// TestZapLogger_LevelAndMessage tests that the wrapper emits the canonical
// level/message JSON schema.
//
// Why this test is important:
//   - The caller-fidelity and stacktrace refactors touch PC recording; this
//     guards that the basic log envelope (level + message keys) did not regress
//     as a side effect, since downstream log parsing depends on those fields.
//
// What it tests:
//   - A Warn call produces a record with level="warn" and the given message.
func TestZapLogger_LevelAndMessage(t *testing.T) {
	out := zapCaptureStdout(t, func() {
		zaplogger.New(zaplogger.Config{Level: "info", Format: "json", RedactPII: false}).
			Warn("careful")
	})
	rec := zapLastJSONLine(t, out)
	assert.Equal(t, "warn", rec["level"])
	assert.Equal(t, "careful", rec["message"])
}

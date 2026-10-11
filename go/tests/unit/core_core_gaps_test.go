// Package core_test closes the remaining coverage gaps in go/core: the
// upstream HTTP-status branch, the empty-stack and stack-preservation paths of
// AppError, the wrapped-cause branches of the Zap marshallers, and the
// transaction-context and span-kind option helpers in core/interfaces. Every
// test is black-box (external package) and asserts observable behaviour only.
package unit_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// TestToHTTPStatusUpstream tests that the CodeUpstream error code maps to HTTP
// 502 Bad Gateway.
//
// Why this test is important:
//   - An upstream-dependency failure must surface to clients as 502, not the
//     500 default; a wrong mapping mis-signals whether the fault is ours or a
//     downstream's, breaking client retry and on-call triage.
//
// What it tests:
//   - ToHTTPStatus(CodeUpstream) returns http.StatusBadGateway (502).
func TestToHTTPStatusUpstream(t *testing.T) {
	t.Parallel()

	assert.Equal(t, http.StatusBadGateway, apperr.ToHTTPStatus(apperr.CodeUpstream))
}

// TestStackTraceNoStack tests that an AppError with no captured stack renders an
// empty trace.
//
// Why this test is important:
//   - AppErrors constructed as bare struct literals (not via New/Wrap) carry no
//     stack; StackTrace must return "" rather than panic on the nil slice, so
//     the Zap marshallers safely omit the stack field for such errors.
//
// What it tests:
//   - (&AppError{}).StackTrace() returns the empty string.
func TestStackTraceNoStack(t *testing.T) {
	t.Parallel()

	err := &apperr.AppError{Code: apperr.CodeInternal, Message: "no stack"}

	assert.Empty(t, err.StackTrace())
}

// TestWrapPreservesOriginStack tests that wrapping an existing AppError
// preserves the origin stack instead of re-capturing at the wrap site.
//
// Why this test is important:
//   - The value of a stack trace is that it points at where the failure
//     originated; if Wrap re-captured, every wrap would relocate the trace to
//     the wrap site and hide the true origin, defeating diagnosis.
//
// What it tests:
//   - The StackTrace of Wrap(origin, ...) equals the origin's StackTrace.
func TestWrapPreservesOriginStack(t *testing.T) {
	t.Parallel()

	origin := apperr.New(apperr.CodeInternal, "origin")
	originStack := origin.StackTrace()
	require.NotEmpty(t, originStack, "origin error should capture a stack")

	wrapped := apperr.Wrap(origin, apperr.CodeUnavailable, "wrapped")

	var wrappedErr *apperr.AppError
	require.ErrorAs(t, wrapped, &wrappedErr)
	assert.Equal(t, originStack, wrappedErr.StackTrace(),
		"wrapping an AppError should preserve the origin stack")
}

// TestStackTraceStartsAtCaller tests that an AppError's stack trace starts at the code
// that created the error, not inside the errors package.
//
// Why this test is important:
//   - The first frame of a trace is the one a reader looks at; if New/Wrap frames lead
//     the trace, every error appears to originate in the errors package.
//
// What it tests:
//   - The first frame of the trace is this test function for New, for a helper
//     constructor that calls New internally (InvalidInput, Unavailable), and for Wrap of
//     a plain error — each adds errors-package frames the trace must skip.
func TestStackTraceStartsAtCaller(t *testing.T) {
	t.Parallel()

	for name, err := range map[string]*apperr.AppError{
		"New":          apperr.New(apperr.CodeInternal, "origin"),
		"InvalidInput": apperr.InvalidInput("origin"),
		"Unavailable":  apperr.Unavailable("origin"),
	} {
		first, _, _ := strings.Cut(err.StackTrace(), "\n")
		assert.True(t, strings.HasSuffix(first, ".TestStackTraceStartsAtCaller"),
			"%s: first frame = %q, want the calling test function", name, first)
	}

	var wrapped *apperr.AppError
	require.ErrorAs(
		t,
		apperr.Wrap(apperr.Sentinel("cause"), apperr.CodeInternal, "origin"),
		&wrapped,
	)
	first, _, _ := strings.Cut(wrapped.StackTrace(), "\n")
	assert.True(t, strings.HasSuffix(first, ".TestStackTraceStartsAtCaller"),
		"Wrap: first frame = %q, want the calling test function", first)
}

// TestMarshalLogObjectWithCause tests that MarshalLogObject encodes the wrapped
// cause when the AppError wraps an underlying error.
//
// Why this test is important:
//   - The wrapped cause is what makes a generic message diagnosable; a log
//     pipeline that drops it hides the real underlying failure (e.g. the
//     Postgres error behind "failed to query database").
//
// What it tests:
//   - MarshalLogObject writes a "cause" field carrying the wrapped error text.
func TestMarshalLogObjectWithCause(t *testing.T) {
	t.Parallel()

	wrapped := apperr.Wrap(
		apperr.Sentinel("connection refused"),
		apperr.CodeUnavailable,
		"db down",
	)
	appErr := &apperr.AppError{}
	ok := errors.As(wrapped, &appErr)
	require.True(t, ok)

	enc := zapcore.NewMapObjectEncoder()
	require.NoError(t, appErr.MarshalLogObject(enc))

	assert.Equal(t, "connection refused", enc.Fields["cause"])
}

// TestZapFieldsWithCause tests that ZapFields emits an error_cause field when
// the AppError wraps an underlying error.
//
// Why this test is important:
//   - Structured log/alert pipelines key off error_cause to surface the real
//     root failure; omitting it for wrapped errors blinds alerting to the
//     underlying dependency fault.
//
// What it tests:
//   - ZapFields on a wrapped AppError includes an error_cause field.
func TestZapFieldsWithCause(t *testing.T) {
	t.Parallel()

	wrapped := apperr.Wrap(apperr.Sentinel("boom"), apperr.CodeInternal, "op failed")
	appErr := &apperr.AppError{}
	ok := errors.As(wrapped, &appErr)
	require.True(t, ok)

	keys := fieldKeySet(apperr.ZapFields(appErr))

	assert.True(t, keys["error_cause"], "expected error_cause field, got keys %v", keys)
}

// TestInTxContext tests that WithInTx marks a context as in-transaction and InTx
// reads it back, defaulting to false on an unmarked context.
//
// Why this test is important:
//   - Statement-level retry must be skipped inside a transaction (once a
//     statement fails the transaction is aborted); the retry decorator relies on
//     InTx to detect the marker without depending on any concrete ORM, so a
//     wrong reading either masks the real error or retries pointlessly.
//
// What it tests:
//   - InTx(WithInTx(ctx)) is true; InTx(background) is false.
func TestInTxContext(t *testing.T) {
	t.Parallel()

	assert.False(t, interfaces.InTx(context.Background()),
		"a plain context is not in a transaction")
	assert.True(t, interfaces.InTx(interfaces.WithInTx(context.Background())),
		"WithInTx should mark the context as in-transaction")
}

// TestWithSpanKind tests that the WithSpanKind option sets the span kind on the
// span configuration.
//
// Why this test is important:
//   - Span kind (server/client/producer/consumer) drives how a trace backend
//     renders and correlates spans; a dropped option would silently mislabel
//     every span as the default internal kind.
//
// What it tests:
//   - Applying WithSpanKind(kind) sets SpanConfig.Kind to that kind.
func TestWithSpanKind(t *testing.T) {
	t.Parallel()

	var cfg interfaces.SpanConfig
	interfaces.WithSpanKind(interfaces.SpanKindClient)(&cfg)

	assert.Equal(t, interfaces.SpanKindClient, cfg.Kind)
}

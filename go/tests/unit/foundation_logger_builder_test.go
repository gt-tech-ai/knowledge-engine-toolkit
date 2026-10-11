package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestLoggerBuilder_KindZap tests that the logger factory produces a working
// Zap implementation.
//
// Why this test is important:
//   - Zap is the primary production logger; a broken factory means services
//     start without structured logging, losing all observability
//
// What it tests:
//   - logger.New(KindZap) returns a non-nil logger without error
//   - Calling Info with key/value attributes does not panic
func TestLoggerBuilder_KindZap(t *testing.T) {
	t.Parallel()

	l, err := logger.New(logger.KindZap)
	require.NoError(t, err, "logger.New(KindZap) must not return error")
	require.NotNil(t, l, "expected non-nil logger")

	// Verify basic logging works through the interface.
	l.Info("test message", "key", "value")
}

// TestLoggerBuilder_KindStdlib tests that the logger factory produces a
// working stdlib (slog) implementation.
//
// Why this test is important:
//   - The stdlib logger is the fallback for environments where Zap is not
//     available; it must satisfy the same interface contract
//
// What it tests:
//   - logger.New(KindStdlib) returns a non-nil logger without error
//   - Calling Info with key/value attributes does not panic
func TestLoggerBuilder_KindStdlib(t *testing.T) {
	t.Parallel()

	l, err := logger.New(logger.KindStdlib)
	require.NoError(t, err, "logger.New(KindStdlib) must not return error")
	require.NotNil(t, l, "expected non-nil logger")

	l.Info("test message from stdlib", "key", "value")
}

// TestLoggerBuilder_UnknownKindReturnsError tests that the logger factory
// rejects unsupported logger backends.
//
// Why this test is important:
//   - Misconfigured Kind values must fail fast at startup rather than produce
//     a nil logger that panics on the first log call
//
// What it tests:
//   - logger.New(Kind(999)) returns a non-nil error
func TestLoggerBuilder_UnknownKindReturnsError(t *testing.T) {
	t.Parallel()

	_, err := logger.New(logger.Kind(999))
	require.Error(t, err, "expected error for unknown kind")
}

// TestLoggerBuilder_NewFromConfig tests that NewFromConfig creates a logger
// from an explicit config struct.
//
// Why this test is important:
//   - NewFromConfig is the primary constructor used in Wire-injected services;
//     it must work with fully specified config structs from YAML/env
//
// What it tests:
//   - NewFromConfig with KindZap and level "info" returns a non-nil logger without error
func TestLoggerBuilder_NewFromConfig(t *testing.T) {
	t.Parallel()

	cfg := logger.Config{
		Kind:  logger.KindZap,
		Level: "info",
	}

	l, err := logger.NewFromConfig(cfg)
	require.NoError(t, err, "NewFromConfig must not return error")
	require.NotNil(t, l, "expected non-nil logger")
}

// TestLoggerBuilder_DefaultConfig tests that the default logger config
// provides production-ready settings.
//
// Why this test is important:
//   - Services that omit explicit logger config inherit these defaults; an
//     empty level string would cause a parse error or log at the wrong level
//
// What it tests:
//   - Default kind is KindZap
//   - Default level is non-empty
func TestLoggerBuilder_DefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := logger.DefaultConfig()
	assert.Equal(t, logger.KindZap, cfg.Kind, "default kind should be KindZap")
	assert.NotEmpty(t, cfg.Level, "default level must be non-empty")
}

// TestLoggerBuilder_WithOptions tests that the factory accepts functional
// options to customize the logger.
//
// Why this test is important:
//   - Debug-level logging is essential for local development and troubleshooting;
//     if the WithLevel option silently fails, developers lose verbose output
//
// What it tests:
//   - logger.New with WithLevel("debug") returns a non-nil logger without error
func TestLoggerBuilder_WithOptions(t *testing.T) {
	t.Parallel()

	l, err := logger.New(logger.KindZap, logger.WithLevel("debug"))
	require.NoError(t, err, "logger.New with WithLevel must not return error")
	require.NotNil(t, l, "expected non-nil logger")
}

// TestLogger_SharedUtilitiesStillWork tests that shared logger utilities like
// PII redaction work through the factory package.
//
// Why this test is important:
//   - PII redaction is a compliance requirement (GDPR, SOC2); the factory
//     re-exports RedactPII so callers do not need to import sub-packages
//
// What it tests:
//   - RedactPII on an email address returns a string containing "[REDACTED]"
//   - The original email string is not present in the output
func TestLogger_SharedUtilitiesStillWork(t *testing.T) {
	t.Parallel()

	redacted := logger.RedactPII("my email is test@example.com")
	assert.NotEqual(t, "my email is test@example.com", redacted, "PII must be redacted")
	assert.Contains(
		t,
		redacted, "[REDACTED]",
		"expected [REDACTED] in output, got %s",
		redacted,
	)
}

// TestLogger_MockAsConsumerDependency tests that MockLogger satisfies
// interfaces.Logger and can be used by consumers that depend on the interface.
//
// Why this test is important:
//   - Service-layer unit tests mock the logger; the mock must implement Info,
//     Error, and With or those tests cannot compile
//   - Validates the mock contract so upstream tests can trust it
//
// What it tests:
//   - MockLogger assigned to interfaces.Logger compiles
//   - Info, Error, and With delegate to the mock as expected
func TestLogger_MockAsConsumerDependency(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mock := mocks.NewMockLogger(ctrl)

	mock.EXPECT().Info("request handled", "status", 200)
	mock.EXPECT().Error("request failed", "error", "timeout")
	mock.EXPECT().With("service", "api").Return(mock)

	var l interfaces.Logger = mock
	l.Info("request handled", "status", 200)
	l.Error("request failed", "error", "timeout")
	child := l.With("service", "api")
	require.NotNil(t, child, "With must return non-nil child logger")
}

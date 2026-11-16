package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/logger"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// schemaV2TimestampLayout is the v2 timestamp shape: RFC 3339, UTC, milliseconds.
const schemaV2TimestampLayout = "2006-01-02T15:04:05.000Z"

// schemaLogKinds names each logger kind the schema tests run against.
func schemaLogKinds() map[string]logger.Kind {
	return map[string]logger.Kind{
		"zap":    logger.KindZap,
		"stdlib": logger.KindStdlib,
	}
}

// schemaV2ErrorLine logs one coded error through a v2 logger of kind and
// returns the parsed JSON line.
func schemaV2ErrorLine(t *testing.T, kind logger.Kind) map[string]any {
	t.Helper()

	out := zapCaptureStdout(t, func() {
		currentLogger, err := logger.New(kind,
			logger.WithSchema(logger.SchemaV2),
			logger.WithService("svc-a"),
			logger.WithEnv("env-a"),
			logger.WithRedactPII(false),
		)
		require.NoError(t, err)

		cause := apperr.Sentinel("upstream refused")
		currentLogger.Error("call failed", textError,
			apperr.Wrap(cause, apperr.CodeUnavailable, "store call"))
	})

	return zapLastJSONLine(t, out)
}

// TestLoggerSchemaV2_ErrorLineCarriesFieldSet tests that the v2 schema emits the
// unified field set on an error line from either logger kind.
//
// Why this test is important:
//   - Dashboards, alerts and log queries key on one field set across every service
//     and language; a backend that drifts (a nested error object, a microsecond
//     timestamp, a missing service) breaks every query that filters on it.
//
// What it tests:
//   - service and env carry the configured values; level is "error".
//   - timestamp parses as RFC 3339 UTC with exactly millisecond precision.
//   - error is the error's message (cause included) as a string, and error_code
//     is the AppError's code.
//   - caller is a "file:line" string pointing at this test file; stacktrace is set.
func TestLoggerSchemaV2_ErrorLineCarriesFieldSet(t *testing.T) {
	t.Parallel()

	for name, kind := range schemaLogKinds() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if !fixtures.InReexec() {
				fixtures.ReexecTest(
					t,
					"TestLoggerSchemaV2_ErrorLineCarriesFieldSet/"+name,
				)

				return
			}

			rec := schemaV2ErrorLine(t, kind)

			assert.Equal(t, "svc-a", rec["service"])
			assert.Equal(t, "env-a", rec["env"])
			assert.Equal(t, "error", rec["level"])
			assert.Equal(t, "call failed", rec["message"])

			stamp := stdx.As[string](rec["timestamp"])
			_, parseErr := time.Parse(schemaV2TimestampLayout, stamp)
			require.NoErrorf(t, parseErr, "timestamp %q is not ms-precision UTC", stamp)

			assert.Equal(t, string(apperr.CodeUnavailable), rec["error_code"])

			errText, ok := rec[textError].(string)
			require.Truef(t, ok, "error should be a string, got %T", rec[textError])
			assert.Contains(t, errText, "upstream refused")

			caller := stdx.As[string](rec["caller"])
			assert.Contains(t, caller, "logger_schema_test.go:")
			assert.NotEmpty(t, rec["stacktrace"])
		})
	}
}

// TestLoggerSchemaV1_DefaultKeepsShape tests that a logger built without a schema
// keeps today's output.
//
// Why this test is important:
//   - v2 is opt-in: a consumer that has not moved its dashboards must see no
//     change in the lines it already parses.
//
// What it tests:
//   - With no schema set, a zap error line still nests the AppError as an object
//     and carries no service, env or error_code field.
func TestLoggerSchemaV1_DefaultKeepsShape(t *testing.T) {
	t.Parallel()

	if !fixtures.InReexec() {
		fixtures.ReexecTest(t, "TestLoggerSchemaV1_DefaultKeepsShape")

		return
	}

	out := zapCaptureStdout(t, func() {
		l, err := logger.New(logger.KindZap, logger.WithRedactPII(false))
		require.NoError(t, err)
		l.Error("call failed", textError,
			apperr.New(apperr.CodeUnavailable, "store call"))
	})
	rec := zapLastJSONLine(t, out)

	assert.IsType(t, map[string]any{}, rec[textError])
	assert.NotContains(t, rec, "service")
	assert.NotContains(t, rec, "env")
	assert.NotContains(t, rec, "error_code")
}

// TestLoggerNewFromConfig_UnknownSchema tests that an unknown schema fails loudly.
//
// Why this test is important:
//   - A typo in the schema setting must stop boot, not silently fall back to v1
//     while the operator believes v2 is live.
//
// What it tests:
//   - NewFromConfig with Schema "v3" returns a CodeInvalidInput error.
func TestLoggerNewFromConfig_UnknownSchema(t *testing.T) {
	t.Parallel()

	cfg := logger.DefaultConfig()

	cfg.Schema = "v3"

	_, err := logger.NewFromConfig(&cfg)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}

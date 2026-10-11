// Package transport_test provides tests for BaseController HTTP response methods.
package unit_test

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/rest"
)

// ---------------------------------------------------------------------------
// MapErrorToHTTPStatus
// ---------------------------------------------------------------------------

// TestMapErrorToHTTPStatus_AppError tests that each AppError category maps to the HTTP
// status code clients depend on.
//
// Why this test is important:
//   - The error-code-to-HTTP-status mapping is the public contract every API
//     consumer relies on to branch (retry on 503, re-auth on 401, surface 404).
//     A silent drift in the mapping would mislead callers without any compile error.
//
// What it tests:
//   - NOT_FOUND→404, UNAUTHORIZED→401, FORBIDDEN→403, INVALID_INPUT→400,
//     CONFLICT→409, TIMEOUT→504, INTERNAL→500, and UNAVAILABLE→503.
func TestMapErrorToHTTPStatus_AppError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err      error
		name     string
		expected int
	}{
		{
			err:      apperr.NotFound("not found"),
			name:     "not found",
			expected: http.StatusNotFound,
		},
		{
			err:      apperr.Unauthorized("unauthorized"),
			name:     "unauthorized",
			expected: http.StatusUnauthorized,
		},
		{
			err:      apperr.Forbidden("forbidden"),
			name:     "forbidden",
			expected: http.StatusForbidden,
		},
		{
			err:      apperr.InvalidInput("bad input"),
			name:     "invalid input",
			expected: http.StatusBadRequest,
		},
		{
			err:      apperr.Conflict("conflict"),
			name:     "conflict",
			expected: http.StatusConflict,
		},
		{
			err:      apperr.Timeout("timeout"),
			name:     "timeout",
			expected: http.StatusGatewayTimeout,
		},
		{
			err:      apperr.Internal("internal"),
			name:     "internal",
			expected: http.StatusInternalServerError,
		},
		{
			err:      apperr.Unavailable("unavailable"),
			name:     "unavailable",
			expected: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status := rest.MapErrorToHTTPStatus(tt.err)
			assert.Equal(t, tt.expected, status)
		})
	}
}

// TestMapErrorToHTTPStatus_PlainError tests that an error carrying no AppError code falls
// back to 500.
//
// Why this test is important:
//   - Unrecognized errors (raw Go errors, wrapped infra failures) must never
//     leak as a 200 or an arbitrary status; defaulting to 500 keeps the
//     mapping total and prevents an unclassified error from being treated as success.
//
// What it tests:
//   - A plain Go error with no AppError code maps to HTTP 500.
func TestMapErrorToHTTPStatus_PlainError(t *testing.T) {
	t.Parallel()

	status := rest.MapErrorToHTTPStatus(assert.AnError)
	assert.Equal(t, http.StatusInternalServerError, status)
}

// ---------------------------------------------------------------------------
// WriteJSON
// ---------------------------------------------------------------------------

// TestWriteJSON_Success tests that WriteJSON emits the chosen status, JSON Content-Type,
// and serialized body.
//
// Why this test is important:
//   - WriteJSON is the single chokepoint every successful REST response flows
//     through; if it wrote the wrong status, omitted the JSON Content-Type, or
//     dropped fields, every endpoint built on BaseController would break at once.
//
// What it tests:
//   - The response carries status 200, Content-Type application/json, and a
//     body that round-trips back to the supplied key/value.
func TestWriteJSON_Success(t *testing.T) {
	t.Parallel()

	c := &rest.BaseController{}
	rec := httptest.NewRecorder()

	data := map[string]string{"key": "value"}
	c.WriteJSON(rec, http.StatusOK, data)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	body, _ := io.ReadAll(rec.Body)
	var result map[string]string
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "value", result["key"])
}

// TestWriteJSON_MarshalError tests that WriteJSON degrades to a safe 500 when the payload
// can't be serialized.
//
// Why this test is important:
//   - A value that fails json.Marshal (e.g. math.Inf) mid-handler must not
//     panic the goroutine or send a half-written body; the controller has to
//     recover into a clean error response so the connection stays well-formed.
//
// What it tests:
//   - Marshalling an un-encodable value yields status 500 with a JSON
//     Content-Type and a generic "internal server error" body.
func TestWriteJSON_MarshalError(t *testing.T) {
	t.Parallel()

	c := &rest.BaseController{}
	rec := httptest.NewRecorder()

	// math.Inf cannot be marshalled to JSON
	c.WriteJSON(rec, http.StatusOK, math.Inf(1))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	body, _ := io.ReadAll(rec.Body)
	assert.Contains(t, string(body), "internal server error")
}

// ---------------------------------------------------------------------------
// WriteError
// ---------------------------------------------------------------------------

// TestWriteError_AppError tests that a client-facing AppError surfaces its status, code,
// and message to the caller.
//
// Why this test is important:
//   - For expected, client-actionable failures (a missing record), the caller
//     needs the real code and message to render a useful error; suppressing them
//     would turn every actionable 4xx into an opaque failure.
//
// What it tests:
//   - A NOT_FOUND AppError produces status 404 with the matching code and the
//     original "user not found" message in the JSON body.
func TestWriteError_AppError(t *testing.T) {
	t.Parallel()

	c := &rest.BaseController{}
	rec := httptest.NewRecorder()

	appErr := apperr.NotFound("user not found")
	c.WriteError(rec, appErr)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	body, _ := io.ReadAll(rec.Body)
	var resp rest.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, string(apperr.CodeNotFound), resp.Code)
	assert.Equal(t, "user not found", resp.Error)
}

// TestWriteError_InternalAppError tests that internal AppErrors are redacted before
// reaching the client.
//
// Why this test is important:
//   - Internal failure detail ("database connection failed") can expose
//     infrastructure topology and aid attackers; the controller must replace it
//     with a generic message so 500s never leak server-side internals.
//
// What it tests:
//   - An INTERNAL AppError produces status 500 with the INTERNAL code but a
//     generic "internal server error" body, never the original detail.
func TestWriteError_InternalAppError(t *testing.T) {
	t.Parallel()

	c := &rest.BaseController{}
	rec := httptest.NewRecorder()

	appErr := apperr.Internal("database connection failed")
	c.WriteError(rec, appErr)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	body, _ := io.ReadAll(rec.Body)
	var resp rest.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, string(apperr.CodeInternal), resp.Code)
	assert.Equal(
		t,
		"internal server error",
		resp.Error,
		"should not leak internal error details",
	)
}

// TestWriteError_PlainError tests that an uncategorized error is treated as internal and
// redacted.
//
// Why this test is important:
//   - A raw Go error escaping a handler carries no client-safe classification;
//     defaulting it to a redacted 500 ensures unanticipated failures can't leak
//     details or masquerade as a different, actionable status.
//
// What it tests:
//   - A plain Go error produces status 500 with a generic "internal server
//     error" body.
func TestWriteError_PlainError(t *testing.T) {
	t.Parallel()

	c := &rest.BaseController{}
	rec := httptest.NewRecorder()

	c.WriteError(rec, assert.AnError)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	body, _ := io.ReadAll(rec.Body)
	var resp rest.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "internal server error", resp.Error)
}

// ---------------------------------------------------------------------------
// WriteSuccess / WriteCreated / WriteNoContent
// ---------------------------------------------------------------------------

// TestWriteSuccess tests that WriteSuccess is the 200-OK convenience wrapper over
// WriteJSON.
//
// Why this test is important:
//   - WriteSuccess is the helper most read endpoints call; pinning its status
//     and Content-Type guards against a regression where the 200 path silently
//     diverges from the documented JSON response contract.
//
// What it tests:
//   - The response carries status 200, Content-Type application/json, and the
//     serialized payload in the body.
func TestWriteSuccess(t *testing.T) {
	t.Parallel()

	c := &rest.BaseController{}
	rec := httptest.NewRecorder()

	c.WriteSuccess(rec, map[string]string{"status": "ok"})

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	body, _ := io.ReadAll(rec.Body)
	assert.Contains(t, string(body), `"status":"ok"`)
}

// TestWriteCreated tests that WriteCreated signals resource creation with a 201 and the
// new entity.
//
// Why this test is important:
//   - 201 (not 200) is the contract for successful creation; clients and REST
//     tooling branch on it, so a regression to 200 would mislead callers about
//     whether a resource was created.
//
// What it tests:
//   - The response carries status 201, Content-Type application/json, and the
//     created resource (its id) in the body.
func TestWriteCreated(t *testing.T) {
	t.Parallel()

	c := &rest.BaseController{}
	rec := httptest.NewRecorder()

	c.WriteCreated(rec, map[string]string{"id": "123"})

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	body, _ := io.ReadAll(rec.Body)
	assert.Contains(t, string(body), `"id":"123"`)
}

// TestWriteNoContent tests that WriteNoContent emits a bodyless 204.
//
// Why this test is important:
//   - 204 is the contract for successful operations with nothing to return
//     (deletes, idempotent updates); writing a body or a different status would
//     break clients that treat 204 as "done, no payload to read".
//
// What it tests:
//   - The response status is 204 No Content.
func TestWriteNoContent(t *testing.T) {
	t.Parallel()

	c := &rest.BaseController{}
	rec := httptest.NewRecorder()

	c.WriteNoContent(rec)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// ---------------------------------------------------------------------------
// WriteError with various error codes
// ---------------------------------------------------------------------------

// TestWriteError_UnauthorizedAppError tests that an auth failure surfaces a 401 with its
// message intact.
//
// Why this test is important:
//   - 401 drives the client's re-authentication flow, and the message
//     distinguishes auth causes; unlike INTERNAL errors these are client-facing
//     and must NOT be redacted, so the original message has to survive.
//
// What it tests:
//   - An UNAUTHORIZED AppError produces status 401 with the original "invalid
//     token" message.
func TestWriteError_UnauthorizedAppError(t *testing.T) {
	t.Parallel()

	c := &rest.BaseController{}
	rec := httptest.NewRecorder()

	appErr := apperr.Unauthorized("invalid token")
	c.WriteError(rec, appErr)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	body, _ := io.ReadAll(rec.Body)
	var resp rest.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "invalid token", resp.Error)
}

// TestWriteError_ConflictAppError tests that a conflict surfaces a 409 with its message
// intact.
//
// Why this test is important:
//   - 409 tells the client a write lost to a uniqueness/version conflict rather
//     than a transient failure, so it must not be retried blindly; preserving
//     the message ("duplicate key") lets the caller explain the collision.
//
// What it tests:
//   - A CONFLICT AppError produces status 409 with the original "duplicate key"
//     message.
func TestWriteError_ConflictAppError(t *testing.T) {
	t.Parallel()

	c := &rest.BaseController{}
	rec := httptest.NewRecorder()

	appErr := apperr.Conflict("duplicate key")
	c.WriteError(rec, appErr)

	assert.Equal(t, http.StatusConflict, rec.Code)

	body, _ := io.ReadAll(rec.Body)
	var resp rest.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, "duplicate key", resp.Error)
}

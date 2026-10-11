package unit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/rest"
)

// ---------------------------------------------------------------------------
// Adapt
// ---------------------------------------------------------------------------

// TestAdapt_Success tests that Adapt runs the parse→handle→write pipeline and serializes
// the typed response.
//
// Why this test is important:
//   - Adapt is the generic bridge every REST endpoint is built on; if it failed
//     to invoke the typed handler or to write its result as JSON with the
//     configured success status, every adapted route would return wrong data.
//
// What it tests:
//   - On a parse and handler that both succeed, the response is status 200 with
//     a JSON Content-Type and the handler's typed result in the body.
func TestAdapt_Success(t *testing.T) {
	t.Parallel()

	type Req struct{ ID string }
	type Resp struct {
		Name string `json:"name"`
	}

	bc := &rest.BaseController{}
	handler := rest.Adapt(
		bc,
		func(_ context.Context, req Req) (Resp, error) {
			return Resp{Name: "user-" + req.ID}, nil
		},
		http.StatusOK,
		func(r *http.Request) (Req, error) {
			return Req{ID: r.PathValue("id")}, nil
		},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/users/123",
		http.NoBody,
	)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body Resp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "user-", body.Name) // PathValue returns "" without mux routing
}

// TestAdapt_ParseError tests that a parse failure short-circuits before the handler and
// maps to the error status.
//
// Why this test is important:
//   - Request parsing is the input-validation boundary; if Adapt called the
//     business handler with an unparsed/invalid request, malformed input would
//     reach domain logic. The handler must be skipped and the error mapped to HTTP.
//
// What it tests:
//   - When parse returns an INVALID_INPUT error, the handler is never invoked
//     and the response is status 400.
func TestAdapt_ParseError(t *testing.T) {
	t.Parallel()

	type Req struct{ ID string }
	type Resp struct {
		Name string `json:"name"`
	}

	handlerCalled := false
	bc := &rest.BaseController{}
	handler := rest.Adapt(
		bc,
		func(_ context.Context, _ Req) (Resp, error) {
			handlerCalled = true
			return Resp{}, nil
		},
		http.StatusOK,
		func(_ *http.Request) (Req, error) {
			return Req{}, apperr.InvalidInput("missing id")
		},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/users/",
		http.NoBody,
	)

	handler.ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called on parse error")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAdapt_HandlerError tests that an error returned by the business handler is mapped
// to the right HTTP status.
//
// Why this test is important:
//   - Domain errors must flow through the same error-to-HTTP mapping as parse
//     errors so the success status is suppressed; otherwise a handler failure
//     could still be reported to the client as a 200.
//
// What it tests:
//   - When the handler returns a NOT_FOUND error, the response is status 404
//     (not the configured success status).
func TestAdapt_HandlerError(t *testing.T) {
	t.Parallel()

	type Req struct{ ID string }
	type Resp struct {
		Name string `json:"name"`
	}

	bc := &rest.BaseController{}
	handler := rest.Adapt(
		bc,
		func(_ context.Context, _ Req) (Resp, error) {
			return Resp{}, apperr.NotFound("user not found")
		},
		http.StatusOK,
		func(_ *http.Request) (Req, error) {
			return Req{ID: "123"}, nil
		},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/users/123",
		http.NoBody,
	)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// ---------------------------------------------------------------------------
// AdaptNoContent
// ---------------------------------------------------------------------------

// TestAdaptNoContent_Success tests that AdaptNoContent discards the handler result and
// replies 204 with no body.
//
// Why this test is important:
//   - Mutation endpoints (deletes, fire-and-forget updates) advertise a 204
//     no-body contract; if AdaptNoContent serialized the response value or used
//     a different status, clients expecting an empty 204 would mis-parse the reply.
//
// What it tests:
//   - On a successful handler, the response is status 204 with an empty body.
func TestAdaptNoContent_Success(t *testing.T) {
	t.Parallel()

	type Req struct{ ID string }
	type Resp struct{}

	bc := &rest.BaseController{}
	handler := rest.AdaptNoContent(
		bc,
		func(_ context.Context, _ Req) (Resp, error) {
			return Resp{}, nil
		},
		func(_ *http.Request) (Req, error) {
			return Req{ID: "123"}, nil
		},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodDelete,
		"/users/123",
		http.NoBody,
	)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.Bytes())
}

// TestAdaptNoContent_ParseError tests that a parse failure on a no-content route still
// maps to an error status.
//
// Why this test is important:
//   - The 204 path must not swallow input-validation failures; an invalid
//     request has to surface as an error rather than a misleading "success with
//     no content", or callers would believe a rejected mutation actually ran.
//
// What it tests:
//   - When parse returns an INVALID_INPUT error, the response is status 400
//     (not 204).
func TestAdaptNoContent_ParseError(t *testing.T) {
	t.Parallel()

	type Req struct{ ID string }
	type Resp struct{}

	bc := &rest.BaseController{}
	handler := rest.AdaptNoContent(
		bc,
		func(_ context.Context, _ Req) (Resp, error) {
			return Resp{}, nil
		},
		func(_ *http.Request) (Req, error) {
			return Req{}, apperr.InvalidInput("bad request")
		},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodDelete,
		"/users/",
		http.NoBody,
	)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAdaptNoContent_HandlerError tests that a handler error on a no-content route is
// mapped to a failure status.
//
// Why this test is important:
//   - Even when success returns no body, a handler failure must override the 204
//     path; an uncategorized error (a plain Go error here) has to become a 500
//     rather than a false 204, so clients don't treat a failed mutation as done.
//
// What it tests:
//   - When the handler returns a plain Go error, the response is status 500.
func TestAdaptNoContent_HandlerError(t *testing.T) {
	t.Parallel()

	type Req struct{ ID string }
	type Resp struct{}

	bc := &rest.BaseController{}
	handler := rest.AdaptNoContent(
		bc,
		func(_ context.Context, _ Req) (Resp, error) {
			return Resp{}, apperr.Sentinel("database error")
		},
		func(_ *http.Request) (Req, error) {
			return Req{ID: "123"}, nil
		},
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodDelete,
		"/users/123",
		http.NoBody,
	)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

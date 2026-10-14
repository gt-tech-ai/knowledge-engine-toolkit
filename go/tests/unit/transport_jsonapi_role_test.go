package unit_test

// Black-box tests for the role-driven JSON:API middleware.
// They drive jsonapi.Middleware over an httptest handler with a synthetic
// operation-keyed route table (a pure HTTP transform — no external deps),
// asserting the per-role response envelope + the inbound request-envelope
// validation that replace the old (method, ResourceID, data-presence) heuristics.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	codec "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/jsonapi"
	jsonapi "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/jsonapi"
)

// docRoutes is the synthetic /api/v1 route table exercised by the role tests: a
// documents resource with LIST / GET_ONE / CREATE / UPDATE / DELETE, a resource-
// shaped POST ACTION (confirm), and a literal action colliding with a {param}.
func docRoutes() codec.Config {
	return codec.Config{
		BasePath: "/api/v1",
		Routes: []codec.RouteEntry{
			{
				Method:        "GET",
				PathTemplate:  "/api/v1/documents",
				Role:          codec.RoleList,
				ResourceType:  "documents",
				CollectionKey: "documents",
				PaginationKey: "pagination",
			},
			{
				Method:          "POST",
				PathTemplate:    "/api/v1/documents",
				Role:            codec.RoleCreate,
				ResourceType:    "documents",
				SingleKey:       "document",
				RequestEnvelope: true,
			},
			{
				Method:       "GET",
				PathTemplate: "/api/v1/documents/{document_id}",
				Role:         codec.RoleGetOne,
				ResourceType: "documents",
				SingleKey:    "document",
			},
			{
				Method:          "PATCH",
				PathTemplate:    "/api/v1/documents/{document_id}",
				Role:            codec.RoleUpdate,
				ResourceType:    "documents",
				SingleKey:       "document",
				RequestEnvelope: true,
			},
			{
				Method:       "DELETE",
				PathTemplate: "/api/v1/documents/{document_id}",
				Role:         codec.RoleDelete,
			},
			{
				Method:       "POST",
				PathTemplate: "/api/v1/documents/{document_id}/confirm",
				Role:         codec.RoleAction,
				ResourceType: "documents",
				SingleKey:    "document",
			},
		},
	}
}

// jsonHandler returns an http.Handler that writes the given status + JSON body,
// standing in for the Vanguard-transcoded inner handler.
func jsonHandler(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// doREST issues a request through the middleware and returns the recorded result.
func doREST(
	t *testing.T,
	cfg codec.Config,
	inner http.Handler,
	method, path, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody io.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	}
	r := httptest.NewRequestWithContext(t.Context(), method, path, reqBody)
	// Mark as a REST request (not Connect/gRPC) so the middleware transforms it.
	r.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	jsonapi.Middleware(inner, cfg).ServeHTTP(rec, r)
	return rec
}

// TestJSONAPIMatchRoute_MethodPathAndPrecedence tests (method, path) matching and
// the literal-beats-{param} precedence rule.
//
// Why this test is important:
//   - The middleware transforms by the matched route's Role; a wrong match (e.g.
//     an action colliding with an instance {id}) would apply the wrong envelope.
//
// What it tests:
//   - GET/POST on the same path resolve to different roles; a literal segment
//     (/documents/{id}/confirm) beats the {param} instance route; a nested
//     template matches; an unknown path returns false.
func TestJSONAPIMatchRoute_MethodPathAndPrecedence(t *testing.T) {
	t.Parallel()
	cfg := docRoutes()

	get, ok := cfg.MatchRoute("GET", "/api/v1/documents")
	require.True(t, ok)
	assert.Equal(t, codec.RoleList, get.Role)

	post, ok := cfg.MatchRoute("POST", "/api/v1/documents")
	require.True(t, ok)
	assert.Equal(
		t,
		codec.RoleCreate,
		post.Role,
		"same path, different method → different role",
	)

	one, ok := cfg.MatchRoute("GET", "/api/v1/documents/d1")
	require.True(t, ok)
	assert.Equal(t, codec.RoleGetOne, one.Role)

	// Literal "confirm" beats the {document_id} instance route for the same POST.
	confirm, ok := cfg.MatchRoute("POST", "/api/v1/documents/d1/confirm")
	require.True(t, ok)
	assert.Equal(t, codec.RoleAction, confirm.Role, "literal segment beats {param}")

	_, ok = cfg.MatchRoute("GET", "/api/v1/unknown")
	assert.False(t, ok, "unknown path does not match")
}

// TestJSONAPI_CreateMissingDataReturns500 is the regression pin.
//
// Why this test is important:
//   - Previously a create whose handler omitted the resource key silently
//     returned a meta-only 200, contradicting the generated client's data-required
//     type. It must now fail honestly as a 500.
//
// What it tests:
//   - A CREATE route whose handler returns a body without the single resource key
//     yields 500 with a JSON:API errors[] envelope, not a 200 meta document.
func TestJSONAPI_CreateMissingDataReturns500(t *testing.T) {
	t.Parallel()
	rec := doREST(t, docRoutes(), jsonHandler(http.StatusOK, `{"status":"ok"}`),
		http.MethodPost, "/api/v1/documents",
		`{"data":{"type":"documents","attributes":{"title":"x"}}}`)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Contains(t, out, "errors")
	assert.NotContains(t, out, "data")
}

// TestJSONAPI_ActionIsMetaOnly200 tests that an ACTION route is meta-only.
//
// Why this test is important:
//   - A resource-shaped action response must not be wrapped as a data resource
//     (the old data-presence guess would have done so).
//
// What it tests:
//   - A POST ACTION route whose response carries a resource key still yields a
//     200 with meta and no data member.
func TestJSONAPI_ActionIsMetaOnly200(t *testing.T) {
	t.Parallel()
	rec := doREST(t,
		docRoutes(),
		jsonHandler(http.StatusOK, `{"document":{"id":"d1"},"processed":true}`),
		http.MethodPost,
		"/api/v1/documents/d1/confirm",
		"",
	)

	assert.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Contains(t, out, "meta")
	assert.NotContains(t, out, "data", "an ACTION is meta-only, never a data resource")
}

// TestJSONAPI_RolesProduceExpectedEnvelope tests the status + body shape per role.
//
// Why this test is important:
//   - The role→contract FSM is the whole point of the route table; each role must produce
//     its exact status and envelope deterministically.
//
// What it tests:
//   - GET_ONE → 200 {data:object}; LIST → 200 {data:[]}+meta; UPDATE → 200
//     {data:object}; DELETE → 204 no body.
func TestJSONAPI_RolesProduceExpectedEnvelope(t *testing.T) {
	t.Parallel()
	cfg := docRoutes()

	getOne := doREST(t,
		cfg,
		jsonHandler(http.StatusOK, `{"document":{"id":"d1","title":"t"}}`),
		http.MethodGet,
		"/api/v1/documents/d1",
		"",
	)
	assert.Equal(t, http.StatusOK, getOne.Code)
	data := envData(t, getOne)
	assert.Equal(t, "documents", data["type"])

	list := doREST(t,
		cfg,
		jsonHandler(
			http.StatusOK,
			`{"documents":[{"id":"d1"}],"pagination":{"totalCount":1}}`,
		),
		http.MethodGet,
		"/api/v1/documents",
		"",
	)
	assert.Equal(t, http.StatusOK, list.Code)
	var listOut map[string]any
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listOut))
	_, isArr := listOut["data"].([]any)
	assert.True(t, isArr, "LIST data must be an array")
	assert.Contains(t, listOut, "meta")

	upd := doREST(t,
		cfg,
		jsonHandler(http.StatusOK, `{"document":{"id":"d1","title":"new"}}`),
		http.MethodPatch,
		"/api/v1/documents/d1",
		`{"data":{"type":"documents","id":"d1","attributes":{"title":"new"}}}`,
	)
	assert.Equal(t, http.StatusOK, upd.Code)
	assert.Equal(t, "documents", envData(t, upd)["type"])

	del := doREST(t,
		cfg,
		jsonHandler(http.StatusOK, `{}`),
		http.MethodDelete,
		"/api/v1/documents/d1",
		"",
	)
	assert.Equal(t, http.StatusNoContent, del.Code)
	assert.Empty(t, del.Body.Bytes(), "DELETE has no body")
}

// TestJSONAPI_CreateSuccessReturns201WithLocation tests the CREATE happy path.
//
// Why this test is important:
//   - A successful create must be 201 with a Location header derived from the new
//     resource id — the client relies on both.
//
// What it tests:
//   - A CREATE whose handler returns the resource yields 201, a data object, and
//     a Location header ending in the new id.
func TestJSONAPI_CreateSuccessReturns201WithLocation(t *testing.T) {
	t.Parallel()
	rec := doREST(t,
		docRoutes(),
		jsonHandler(http.StatusOK, `{"document":{"id":"d9","title":"x"}}`),
		http.MethodPost,
		"/api/v1/documents",
		`{"data":{"type":"documents","attributes":{"title":"x"}}}`,
	)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "documents", envData(t, rec)["type"])
	assert.Equal(t, "/api/v1/documents/d9", rec.Header().Get("Location"))
}

// TestJSONAPI_CreateRequestWrongTypeReturns400 tests inbound envelope validation.
//
// Why this test is important:
//   - The middleware now validates the request envelope before the handler; a
//     mismatched/absent envelope must fail as a 400, not reach the handler.
//
// What it tests:
//   - A CREATE with data.type ≠ resourceType, and one with no data member, each
//     yield 400 errors[] and the inner handler is never invoked.
func TestJSONAPI_CreateRequestWrongTypeReturns400(t *testing.T) {
	t.Parallel()
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	wrongType := doREST(t, docRoutes(), inner, http.MethodPost, "/api/v1/documents",
		`{"data":{"type":"widgets","attributes":{"title":"x"}}}`)
	assert.Equal(t, http.StatusBadRequest, wrongType.Code)
	assert.Contains(t, wrongType.Body.String(), "errors")

	noData := doREST(t,
		docRoutes(),
		inner,
		http.MethodPost,
		"/api/v1/documents",
		`{"title":"x"}`,
	)
	assert.Equal(t, http.StatusBadRequest, noData.Code)

	assert.False(
		t,
		called,
		"a bad request envelope must be rejected before the handler runs",
	)
}

// TestJSONAPI_CreateRequestValidEnvelopeUnwrapped tests that a valid envelope is
// unwrapped to flat attributes for the handler, and an ACTION body passes through.
//
// Why this test is important:
//   - The handler (and its protovalidate) operate on the flat message; the
//     middleware must unwrap {data:{attributes}} → attributes, and must not
//     unwrap non-enveloped (ACTION) bodies.
//
// What it tests:
//   - A valid CREATE envelope reaches the handler as flat attributes (with data.id
//     merged for the write); an ACTION POST body reaches the handler unchanged.
func TestJSONAPI_CreateRequestValidEnvelopeUnwrapped(t *testing.T) {
	t.Parallel()
	var gotCreate, gotAction map[string]any
	capture := func(dst *map[string]any) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, dst)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"document":{"id":"d1"}}`))
		})
	}

	doREST(t, docRoutes(), capture(&gotCreate), http.MethodPatch, "/api/v1/documents/d1",
		`{"data":{"type":"documents","id":"d1","attributes":{"title":"new"}}}`)
	assert.Equal(t, "new", gotCreate["title"], "handler sees unwrapped attributes")
	assert.Equal(
		t,
		"d1",
		gotCreate["id"],
		"data.id merged into the unwrapped body for the write",
	)

	doREST(t,
		docRoutes(),
		capture(&gotAction),
		http.MethodPost,
		"/api/v1/documents/d1/confirm",
		`{"reason":"looks good"}`,
	)
	assert.Equal(
		t,
		"looks good",
		gotAction["reason"],
		"ACTION body passes through unwrapped-free",
	)
}

// TestJSONAPI_ProtovalidateRunsPostUnwrap tests that a handler-side field-rule
// error on the unwrapped message still surfaces (envelope vs field rules compose).
//
// Why this test is important:
//   - The envelope check must not swallow or duplicate protovalidate; a valid
//     envelope with an invalid attribute must still fail with the handler's error.
//
// What it tests:
//   - A valid CREATE envelope whose (unwrapped) attributes fail a downstream
//     validator (the inner handler returns 400) surfaces as a JSON:API 400.
func TestJSONAPI_ProtovalidateRunsPostUnwrap(t *testing.T) {
	t.Parallel()
	// Inner handler stands in for protovalidate: rejects the unwrapped message
	// when a required attribute is empty.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if title, _ := m["title"].(string); title == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(
				[]byte(`{"code":"invalid_argument","message":"title required"}`),
			)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"document":{"id":"d1"}}`))
	})

	rec := doREST(t, docRoutes(), inner, http.MethodPost, "/api/v1/documents",
		`{"data":{"type":"documents","attributes":{"title":""}}}`)
	assert.Equal(
		t,
		http.StatusBadRequest,
		rec.Code,
		"field-rule error survives past the envelope check",
	)
	assert.Contains(t, rec.Body.String(), "errors")
}

// envData returns the response's data object (asserting it is one).
func envData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	data, ok := out["data"].(map[string]any)
	require.True(t, ok, "response has a data object: %s", rec.Body.String())
	return data
}

// TestJSONAPI_PassthroughCases tests the paths where the middleware must NOT
// transform: non-REST (Connect/gRPC) requests, unmatched REST paths, handler
// error responses, oversized bodies, and unparseable bodies.
//
// Why this test is important:
//   - The middleware sits in front of all traffic; transforming a Connect/gRPC
//     call, an unknown path, or an error/oversized/garbage body would corrupt it.
//
// What it tests:
//   - A Connect request (application/connect+json) passes through byte-for-byte;
//     an unmatched REST path passes through; a handler 4xx becomes a JSON:API
//     errors[] envelope. (Oversized/unparseable-body passthrough is covered in
//     transport_jsonapi_jsonapi_gaps_test.go.)
func TestJSONAPI_PassthroughCases(t *testing.T) {
	t.Parallel()
	cfg := docRoutes()

	// Connect request → passthrough (Connect announces itself via this header).
	connectReq := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/documents",
		strings.NewReader(`{"x":1}`),
	)
	connectReq.Header.Set("Connect-Protocol-Version", "1")
	connectRec := httptest.NewRecorder()
	jsonapi.Middleware(jsonHandler(http.StatusOK, `{"raw":true}`), cfg).
		ServeHTTP(connectRec, connectReq)
	assert.JSONEq(
		t,
		`{"raw":true}`,
		connectRec.Body.String(),
		"Connect request passes through untransformed",
	)

	// Unmatched REST path → passthrough.
	unmatched := doREST(t,
		cfg,
		jsonHandler(http.StatusOK, `{"raw":true}`),
		http.MethodGet,
		"/api/v1/unknown",
		"",
	)
	assert.JSONEq(
		t,
		`{"raw":true}`,
		unmatched.Body.String(),
		"unmatched path passes through",
	)

	// Handler error → JSON:API errors[] envelope.
	errResp := doREST(t,
		cfg,
		jsonHandler(
			http.StatusForbidden,
			`{"code":"permission_denied","message":"nope"}`,
		),
		http.MethodGet,
		"/api/v1/documents/d1",
		"",
	)
	assert.Equal(t, http.StatusForbidden, errResp.Code)
	assert.Contains(t, errResp.Body.String(), "errors")
}

// TestJSONAPI_GetOneMissingResourceReturns500 tests that a GET_ONE/UPDATE whose
// handler omits the declared resource key fails as a 500 (the same honesty guard
// as CREATE).
//
// Why this test is important:
//   - The generated client marks data required on these routes; a meta-only
//     document would silently contradict its types.
//
// What it tests:
//   - A GET_ONE handler returning a body without the single key yields 500 errors[].
func TestJSONAPI_GetOneMissingResourceReturns500(t *testing.T) {
	t.Parallel()
	rec := doREST(t, docRoutes(), jsonHandler(http.StatusOK, `{"unexpected":true}`),
		http.MethodGet, "/api/v1/documents/d1", "")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "errors")
}

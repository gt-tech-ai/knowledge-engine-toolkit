package unit_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/jsonapi"
)

// TestMiddleware_LargeBodyPassthrough tests that a response body over the 1 MiB
// safety limit is passed through untransformed.
//
// Why this test is important:
//   - Buffering and re-marshalling an arbitrarily large body would let a big
//     upstream response blow up memory in the gateway; the guard bounds that cost by
//     streaming oversized bodies straight through.
//
// What it tests:
//   - A matched LIST route whose inner body exceeds maxBodySize is returned
//     verbatim with the inner status, not wrapped in a JSON:API envelope.
func TestMiddleware_LargeBodyPassthrough(t *testing.T) {
	t.Parallel()
	big := make([]byte, (1<<20)+64) // just over the 1 MiB limit
	for i := range big {
		big[i] = 'a'
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(big)
	})
	handler := jsonapi.Middleware(inner, docRoutes())

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/api/v1/documents",
		http.NoBody,
	)
	req.Header.Set("Accept", "application/json")
	handler.ServeHTTP(rec, req)

	res := rec.Result()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Len(t, body, len(big), "oversized body is streamed through unchanged")
}

// TestMiddleware_ReclassifiesVanguardBindingError tests that a Vanguard request-binding
// 500 (numeric google.rpc.Code UNKNOWN = 2) is corrected to a 400, while a genuine
// handler 500 (Internal = 13) is left alone.
//
// Why this test is important:
//   - Vanguard classifies a malformed REST request it can't BIND (an unknown query/path
//     field, an unparseable value) as code Unknown → HTTP 500, and writes the code as a
//     NUMBER (2), not the string "unknown". A request the server couldn't bind is the
//     CLIENT's fault, so it must be 400 — a 5xx there pollutes error rates + misleads
//     callers. Real application 500s (apperr → Internal = 13) must stay 500 so genuine
//     failures aren't masked. Asserting the numeric shape guards the exact body format
//     the live Vanguard transcoder emits (a string "unknown" would silently never match).
//
// What it tests:
//   - inner 500 + {"code":2} → the middleware responds 400.
//   - inner 500 + {"code":13} → the middleware responds 500 (unchanged).
func TestMiddleware_ReclassifiesVanguardBindingError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code int
		want int
	}{
		{"vanguard binding error (code 2) is a 400", 2, http.StatusBadRequest},
		{"real handler 500 (code 13) stays 500", 13, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write(fmt.Appendf(nil, `{"code":%d,"message":"boom"}`, tc.code))
			})
			handler := jsonapi.Middleware(inner, docRoutes())

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(
				t.Context(),
				http.MethodGet,
				"/api/v1/documents",
				http.NoBody,
			)
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tc.want, rec.Result().StatusCode)
		})
	}
}

// TestMiddleware_UnparseableBodyPassthrough tests that a non-JSON success body on a
// matched route is passed through unchanged.
//
// Why this test is important:
//   - A handler may emit a non-JSON payload (e.g. a raw text/health blob); the
//     middleware must not corrupt or drop it, only skip transformation.
//
// What it tests:
//   - A matched LIST route whose inner body is not valid JSON is returned
//     verbatim with the inner status.
func TestMiddleware_UnparseableBodyPassthrough(t *testing.T) {
	t.Parallel()
	raw := []byte("[this is not json")
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	})
	handler := jsonapi.Middleware(inner, docRoutes())

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/api/v1/documents",
		http.NoBody,
	)
	req.Header.Set("Accept", "application/json")
	handler.ServeHTTP(rec, req)

	res := rec.Result()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, raw, body, "an unparseable body is passed through unchanged")
}

// TestTransformCollection_PreservesNonObjectItems tests that a collection whose
// items are not JSON objects keeps those items verbatim in the data array.
//
// Why this test is important:
//   - A repeated scalar field (or a mixed array) must not be dropped or crash the
//     transform; JSON:API's data array carries whatever the upstream returned when an
//     element cannot be shaped into a resource object.
//
// What it tests:
//   - TransformCollection preserves a non-object element as-is while still shaping the
//     object elements into resource objects.
func TestTransformCollection_PreservesNonObjectItems(t *testing.T) {
	t.Parallel()
	cfg := jsonapi.ResourceConfig{Type: "users", CollectionKey: "users"}
	body := map[string]any{
		"users": []any{
			"scalar-item",
			map[string]any{"id": "1", "email": "a@b.test"},
		},
	}

	result := jsonapi.TransformCollection(body, cfg, "/api/v1/demo/users")

	data, ok := result["data"].([]any)
	require.True(t, ok)
	require.Len(t, data, 2)
	assert.Equal(t, "scalar-item", data[0], "a non-object item is preserved verbatim")
	obj, ok := data[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "users", obj["type"])
	assert.Equal(t, "1", obj["id"])
}

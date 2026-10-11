package unit_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/docs"
)

// ---------------------------------------------------------------------------
// SpecHandler
// ---------------------------------------------------------------------------

// TestSpecHandler_ServesJSONSpec tests that the spec handler serves the OpenAPI bytes
// verbatim with cacheable JSON headers.
//
// Why this test is important:
//   - This endpoint is what Redoc and external API tooling fetch to render the
//     contract; the body must be byte-identical to the embedded spec and carry
//     the JSON Content-Type plus a cache header so clients/CDNs don't re-fetch
//     on every page load.
//
// What it tests:
//   - A GET returns 200, Content-Type application/json, Cache-Control
//     "public, max-age=3600", and a body equal to the supplied spec.
func TestSpecHandler_ServesJSONSpec(t *testing.T) {
	t.Parallel()

	spec := []byte(`{"openapi":"3.0.0","info":{"title":"Test"}}`)
	handler := docs.SpecHandler(spec)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/openapi.json",
		http.NoBody,
	)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"))
	assert.Equal(t, string(spec), rec.Body.String())
}

// TestSpecHandler_EmptySpec tests that an empty spec still produces a well-formed JSON
// response rather than failing.
//
// Why this test is important:
//   - During bootstrap or a misconfigured build the embedded spec may be empty;
//     the handler must still answer 200 with the JSON Content-Type and an empty
//     body instead of erroring, so the docs route never hard-fails.
//
// What it tests:
//   - With an empty spec, a GET returns 200, Content-Type application/json, and
//     an empty body.
func TestSpecHandler_EmptySpec(t *testing.T) {
	t.Parallel()

	handler := docs.SpecHandler([]byte{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/openapi.json",
		http.NoBody,
	)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Empty(t, rec.Body.String())
}

// ---------------------------------------------------------------------------
// RedocHandler
// ---------------------------------------------------------------------------

// TestRedocHandler_RendersHTML tests that the Redoc page renders HTML wired to the spec
// URL and configured title.
//
// Why this test is important:
//   - This is the human-facing API docs page; if the rendered HTML omits the
//     spec URL, the title, or the Redoc script, the docs page loads blank or
//     points at the wrong spec, which no unit of the spec handler would catch.
//
// What it tests:
//   - A GET returns 200 with an HTML Content-Type and cache header, and the body
//     embeds the spec-url, the page title, and the Redoc standalone script.
func TestRedocHandler_RendersHTML(t *testing.T) {
	t.Parallel()

	handler := docs.RedocHandler("/openapi.json", "My API")

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/docs",
		http.NoBody,
	)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"))

	body := rec.Body.String()
	assert.Contains(
		t,
		body, `spec-url="/openapi.json"`,
		"should contain spec URL",
	)
	assert.Contains(t, body, "My API — API Docs", "should contain title")
	assert.Contains(
		t,
		body, "redoc.standalone.js",
		"should include Redoc script",
	)
}

// TestRedocHandler_EscapesTitle tests that a caller-supplied title is HTML-escaped,
// closing an XSS hole.
//
// Why this test is important:
//   - The title is interpolated into the docs HTML; if a service passed an
//     attacker-influenced title unescaped, the docs page would execute injected
//     script. This pins the html/template auto-escaping as a security guarantee.
//
// What it tests:
//   - A title containing a <script> tag is rendered escaped (&lt;script&gt;) and
//     the raw "<script>alert" sequence never appears in the body.
func TestRedocHandler_EscapesTitle(t *testing.T) {
	t.Parallel()

	handler := docs.RedocHandler("/spec.json", `<script>alert("xss")</script>`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/docs",
		http.NoBody,
	)

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	// html/template auto-escapes, so the script tag should be escaped.
	assert.NotContains(
		t,
		body, "<script>alert",
		"title should be HTML-escaped",
	)
	assert.Contains(
		t,
		body, "&lt;script&gt;",
		"should contain escaped script tag",
	)
}

// Package jsonapi provides HTTP middleware that transforms Vanguard REST responses into
// [JSON:API](https://jsonapi.org/format/) envelopes with the foundation/jsonapi codec.
//
// Only REST requests are transformed (IsRESTRequest); Connect, gRPC, and gRPC-Web pass
// through unchanged. The middleware sits between the HTTP server and Vanguard,
// rewriting response bodies into {data, meta, links, errors} and validating/unwrapping
// the inbound {data:{type,attributes}} request envelope, by the Role of the route the
// consumer's table (codec.Config) matches.
package jsonapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	codec "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/jsonapi"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// contentTypeJSONAPI is the JSON:API media type set on all transformed
// responses, so a downstream proxy can detect responses that have already been
// transformed.
const contentTypeJSONAPI = "application/vnd.api+json"

// maxBodySize is a safety limit for response buffering. Responses larger
// than this are passed through without transformation.
const maxBodySize = 1 << 20 // 1 MiB

// Middleware wraps an http.Handler with JSON:API response/request
// transformation, driven by the generated operation-keyed route table. Only REST
// requests are transformed; Connect, gRPC, and gRPC-Web pass through unchanged.
//
// Request handling order is match-first: the route is resolved by (method, path)
// before any body handling, so an unmatched path passes through untouched. A
// matched CREATE/UPDATE route validates + unwraps the inbound
// {data:{type,attributes}} envelope (400 before the handler on a malformed,
// absent, or type-mismatched envelope); other roles pass their body through.
// The response is then transformed by the route's codec.Role — never by guessing
// method/path/data-presence.
func Middleware(
	inner http.Handler,
	cfg codec.Config,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Match first: resolve the route by (method, path) before touching the
		// body. An unmatched path is not part of the JSON:API surface — pass it
		// through unchanged (body untouched).
		var route codec.RouteEntry

		matched := IsRESTRequest(r)
		if matched {
			route, matched = cfg.MatchRoute(r.Method, r.URL.Path)
		}

		if !matched {
			inner.ServeHTTP(w, r)

			return
		}

		serveRoute(inner, route, w, r)
	})
}

// serveRoute unwraps the request, runs the inner handler and envelopes its response.
func serveRoute(
	inner http.Handler,
	route codec.RouteEntry,
	w http.ResponseWriter,
	r *http.Request,
) {
	if route.RequestEnvelope && !unwrapRequest(w, r, route.ResourceType) {
		return
	}

	// Capture the self-link before the inner handler runs, because Vanguard
	// rewrites r.URL.Path from REST to Connect RPC format.
	selfLink := r.URL.Path

	// Disable content encoding so the inner handler returns an uncompressed
	// body we can parse and transform.
	r.Header.Del("Accept-Encoding")

	cw := &captureWriter{w: w, status: http.StatusOK}
	inner.ServeHTTP(cw, r)

	// Error responses (4xx/5xx) — includes a handler-side protovalidate 400.
	if cw.status >= http.StatusBadRequest {
		status := reclassifyBindingError(cw.status, cw.body.Bytes())

		writeJSONAPI(w, status, codec.TransformError(status, cw.body.Bytes()))

		return
	}

	// DELETE → 204 No Content, regardless of the handler's body.
	if route.Role == codec.RoleDelete {
		writeJSONAPI(w, http.StatusNoContent, nil)

		return
	}

	writeResource(w, cw, route, selfLink)
}

// unwrapRequest validates a write op's envelope (CREATE/UPDATE). A malformed, absent, or
// type-mismatched envelope is rejected as a JSON:API 400 before the handler runs; a valid
// envelope is unwrapped so the handler sees the flat attributes (protovalidate still runs
// on that unwrapped message downstream — the envelope and field checks compose, neither
// duplicated). It reports whether the request may proceed.
func unwrapRequest(w http.ResponseWriter, r *http.Request, resourceType string) bool {
	// Server-request Body is always non-nil (net/http guarantees it; empty
	// bodies read as EOF), so ReadAll is safe here.
	body, err := io.ReadAll(r.Body)

	_ = r.Body.Close()

	if err != nil {
		writeRequestError(w, "could not read request body")

		return false
	}

	unwrapped, msg := unwrapAndValidate(body, resourceType)
	if msg != "" {
		writeRequestError(w, msg)

		return false
	}

	r.Body = io.NopCloser(bytes.NewReader(unwrapped))
	r.ContentLength = int64(len(unwrapped))

	return true
}

// writeJSONAPI writes a response under the JSON:API content type.
func writeJSONAPI(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", contentTypeJSONAPI)
	w.Header().Del("Content-Length")
	w.WriteHeader(status)

	if body != nil {
		_, _ = w.Write(body)
	}
}

// passThrough forwards the captured response unchanged.
func passThrough(w http.ResponseWriter, cw *captureWriter) {
	w.WriteHeader(cw.status)

	_, _ = w.Write(cw.body.Bytes())
}

// writeResource envelopes a success body. A body too large to buffer safely, or one
// that does not round-trip as JSON, passes through unchanged.
func writeResource(
	w http.ResponseWriter, cw *captureWriter, route codec.RouteEntry, selfLink string,
) {
	if cw.body.Len() > maxBodySize {
		passThrough(w, cw)

		return
	}

	var parsed map[string]any

	err := json.Unmarshal(cw.body.Bytes(), &parsed)
	if err != nil {
		passThrough(w, cw)

		return
	}

	result, status, handled := transformByRole(w, route, parsed, selfLink)
	if !handled {
		// A missing-resource error was already written.
		return
	}

	out, err := json.Marshal(result)
	if err != nil {
		passThrough(w, cw)

		return
	}

	writeJSONAPI(w, status, out)
}

// transformByRole builds the JSON:API success envelope for a route's codec.Role. It
// returns the transformed document, the HTTP status, and handled=false when it
// has already written a missing-resource 500 to w (CREATE/GET_ONE/UPDATE whose
// handler omitted the declared resource key — the guard: a create can no
// longer silently return meta-only).
func transformByRole(
	w http.ResponseWriter,
	route codec.RouteEntry,
	parsed map[string]any,
	selfLink string,
) (result map[string]any, status int, handled bool) {
	rc := route.Resource()

	switch route.Role {
	case codec.RoleAction:
		// Action endpoints are meta-only — never a data resource.
		return codec.TransformMeta(parsed), http.StatusOK, true

	case codec.RoleList:
		return codec.TransformCollection(parsed, rc, selfLink), http.StatusOK, true

	case codec.RoleCreate:
		return transformCreate(w, rc, parsed, selfLink)

	case codec.RoleGetOne, codec.RoleUpdate, codec.RoleDelete:
		return transformSingle(w, rc, parsed, selfLink)
	default:
		// An undeclared role shares the single-resource read below.
	}

	return transformSingle(w, rc, parsed, selfLink)
}

// transformSingle envelopes a single-resource read, requiring the declared single
// resource key. (DELETE is answered with 204 before the transform; it reaches here only
// if that changes.)
func transformSingle(
	w http.ResponseWriter,
	rc codec.ResourceConfig,
	parsed map[string]any,
	selfLink string,
) (document map[string]any, code int, handled bool) {
	if !requireKey(w, parsed, rc.SingleKey) {
		return nil, 0, false
	}

	return codec.TransformSingleResource(parsed, rc, selfLink), http.StatusOK, true
}

// requireKey writes the missing-resource error when parsed lacks key.
func requireKey(w http.ResponseWriter, parsed map[string]any, key string) bool {
	if hasKey(parsed, key) {
		return true
	}

	writeMissingResourceError(w, key)

	return false
}

// transformCreate envelopes a create. A collection-only bulk create wraps as a
// collection; the common case is a single created resource → 201 + Location.
func transformCreate(
	w http.ResponseWriter,
	rc codec.ResourceConfig,
	parsed map[string]any,
	selfLink string,
) (document map[string]any, code int, handled bool) {
	if rc.SingleKey == "" && rc.CollectionKey != "" {
		if !requireKey(w, parsed, rc.CollectionKey) {
			return nil, 0, false
		}

		return codec.TransformCollection(parsed, rc, selfLink), http.StatusCreated, true
	}

	if !requireKey(w, parsed, rc.SingleKey) {
		return nil, 0, false
	}

	result := codec.TransformSingleResource(parsed, rc, selfLink)

	data := stdx.As[map[string]any](result["data"])
	if id, ok := data["id"].(string); ok && id != "" {
		w.Header().Set("Location", selfLink+"/"+id)
	}

	return result, http.StatusCreated, true
}

// hasKey reports whether a non-empty key is present in the parsed response body.
// An empty key (a resource with no single/collection key) is treated as present
// so the transform proceeds to its meta-only fallback rather than erroring.
func hasKey(parsed map[string]any, key string) bool {
	if key == "" {
		return true
	}

	_, ok := parsed[key]

	return ok
}

// unwrapAndValidate validates an inbound JSON:API request envelope and returns
// the unwrapped attributes body, or a non-empty human-readable message when the
// envelope is malformed/absent/type-mismatched (which the caller writes as a
// 400). A present data.id is threaded into the unwrapped body so Vanguard can
// route an UPDATE to the right resource, mirroring the prior UnwrapRequest.
func unwrapAndValidate(body []byte, resourceType string) (unwrapped []byte, msg string) {
	data, msg := envelopeData(body)
	if msg != "" {
		return nil, msg
	}

	if typ := stdx.As[string](data["type"]); typ != resourceType {
		return nil, "request data.type must be " + resourceType
	}

	attrs, msg := envelopeAttributes(data)
	if msg != "" {
		return nil, msg
	}

	if id, ok := data["id"]; ok {
		attrs["id"] = id
	}

	out, err := json.Marshal(attrs)
	if err != nil {
		return nil, "could not encode request attributes"
	}

	return out, ""
}

// envelopeData parses the document and returns its data object, or why it cannot.
func envelopeData(body []byte) (object map[string]any, problem string) {
	if len(body) == 0 {
		return nil, "request body is required and must be a JSON:API document"
	}

	var parsed map[string]any

	err := json.Unmarshal(body, &parsed)
	if err != nil {
		return nil, "request body is not valid JSON"
	}

	dataRaw, ok := parsed["data"]
	if !ok {
		return nil, "request must be a JSON:API document with a data member"
	}

	data, ok := dataRaw.(map[string]any)
	if !ok {
		return nil, "request data member must be an object"
	}

	return data, ""
}

// envelopeAttributes returns data's attributes object, or why it cannot.
func envelopeAttributes(data map[string]any) (object map[string]any, problem string) {
	attrsRaw, ok := data["attributes"]
	if !ok {
		return nil, "request data.attributes is required"
	}

	attrs, ok := attrsRaw.(map[string]any)
	if !ok {
		return nil, "request data.attributes must be an object"
	}

	return attrs, ""
}

// reclassifyBindingError corrects the HTTP status of a Vanguard transcoding error. When
// Vanguard cannot BIND a REST request to its target message — an unknown query/path
// field, an unparseable value — it classifies the failure as the Connect code Unknown,
// which maps to HTTP 500. But a request the server could not bind is the CLIENT's fault,
// so it must be 400. Application handler errors never use Unknown (they carry apperr
// codes → Internal (13) / NotFound (5) / InvalidArgument (3) → already 400 / …), so a 500
// whose numeric error code is exactly Unknown is unambiguously a Vanguard request-binding
// failure; every other status/code passes through unchanged. Vanguard writes the code as
// a NUMBER (the google.rpc.Code enum, which connect.Code mirrors), not the string
// "unknown".
func reclassifyBindingError(status int, body []byte) int {
	if status != http.StatusInternalServerError {
		return status
	}

	var parsed struct {
		Code int `json:"code"`
	}

	if json.Unmarshal(body, &parsed) == nil && parsed.Code == int(connect.CodeUnknown) {
		return http.StatusBadRequest
	}

	return status
}

// writeRequestError writes a JSON:API 400 error document for a bad inbound
// request envelope, in the same shape codec.TransformError produces.
func writeRequestError(w http.ResponseWriter, message string) {
	body, err := json.Marshal(map[string]any{
		codec.MemberCode: "invalid_argument",
		"message":        message,
	})
	if err != nil {
		body = nil
	}

	w.Header().Set("Content-Type", contentTypeJSONAPI)
	w.Header().Del("Content-Length")
	w.WriteHeader(http.StatusBadRequest)

	_, _ = w.Write(codec.TransformError(http.StatusBadRequest, body))
}

// writeMissingResourceError reports a resource response that omitted its declared
// resource as a server error, in the same JSON:API error shape codec.TransformError
// produces for any other 5xx. The key names the protobuf field that was expected,
// which is config data rather than caller input.
func writeMissingResourceError(w http.ResponseWriter, singleKey string) {
	body, err := json.Marshal(map[string]any{
		codec.MemberCode: "internal",
		"message":        "response carried no " + singleKey + " resource",
	})
	if err != nil {
		body = nil
	}

	w.Header().Set("Content-Type", contentTypeJSONAPI)
	w.Header().Del("Content-Length")
	w.WriteHeader(http.StatusInternalServerError)

	_, _ = w.Write(codec.TransformError(http.StatusInternalServerError, body))
}

// captureWriter intercepts Write and WriteHeader calls from the inner
// handler, buffering the body and status code so the middleware can
// transform the response before sending it to the client.
//
// Header() delegates to the real writer's header map so that:
//   - The inner handler's header modifications are visible to the middleware
//   - Interface assertions (Flusher, etc.) based on the real writer succeed
type captureWriter struct {
	// w is the real response writer; its header map is shared so the inner
	// handler's header writes and interface assertions still work.
	w http.ResponseWriter

	// body buffers the inner handler's response payload for transformation.
	body bytes.Buffer

	// status holds the captured HTTP status code (defaults to 200 OK).
	status int
}

// Header delegates to the real writer's header map.
func (c *captureWriter) Header() http.Header { return c.w.Header() }

// WriteHeader captures the status code instead of sending it immediately.
func (c *captureWriter) WriteHeader(code int) { c.status = code }

// Write buffers the response body instead of sending it immediately.
func (c *captureWriter) Write(b []byte) (int, error) {
	n, err := c.body.Write(b)

	return n, apperr.Wrap(err, apperr.CodeInternal, "buffer response body")
}

// Flush is a no-op that prevents the inner handler from flushing the buffered
// response before the middleware has transformed it.
func (*captureWriter) Flush() {} // no-op; prevents premature flush

// IsRESTRequest returns true if the request appears to be a REST request
// (not Connect, gRPC, or gRPC-Web).
func IsRESTRequest(r *http.Request) bool {
	// Connect protocol announces itself with this header.
	if r.Header.Get("Connect-Protocol-Version") != "" {
		return false
	}

	// gRPC and gRPC-Web use application/grpc* content types.
	ct := r.Header.Get("Content-Type")

	return !strings.HasPrefix(ct, "application/grpc")
}

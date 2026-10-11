package jsonapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"connectrpc.com/connect"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
// The response is then transformed by the route's Role — never by guessing
// method/path/data-presence.
func Middleware(
	inner http.Handler,
	cfg Config,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsRESTRequest(r) {
			inner.ServeHTTP(w, r)
			return
		}

		// Match first: resolve the route by (method, path) before touching the
		// body. An unmatched path is not part of the JSON:API surface — pass it
		// through unchanged (body untouched).
		route, matched := cfg.MatchRoute(r.Method, r.URL.Path)
		if !matched {
			inner.ServeHTTP(w, r)
			return
		}

		// Inbound envelope validation for write ops (CREATE/UPDATE). A malformed,
		// absent, or type-mismatched envelope is rejected as a JSON:API 400 before
		// the handler runs; a valid envelope is unwrapped so the handler sees the
		// flat attributes (protovalidate still runs on that unwrapped message
		// downstream — the envelope and field checks compose, neither duplicated).
		if route.RequestEnvelope {
			// Server-request Body is always non-nil (net/http guarantees it; empty
			// bodies read as EOF), so ReadAll is safe here.
			body, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				writeRequestError(w, "could not read request body")
				return
			}
			unwrapped, msg := unwrapAndValidate(body, route.ResourceType)
			if msg != "" {
				writeRequestError(w, msg)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(unwrapped))
			r.ContentLength = int64(len(unwrapped))
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
		if cw.status >= 400 {
			status := reclassifyBindingError(cw.status, cw.body.Bytes())
			transformed := TransformError(status, cw.body.Bytes())
			w.Header().Set("Content-Type", contentTypeJSONAPI)
			w.Header().Del("Content-Length")
			w.WriteHeader(status)
			_, _ = w.Write(transformed)
			return
		}

		// DELETE → 204 No Content, regardless of the handler's body.
		if route.Role == RoleDelete {
			w.Header().Set("Content-Type", contentTypeJSONAPI)
			w.Header().Del("Content-Length")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Guard against very large bodies.
		if cw.body.Len() > maxBodySize {
			w.WriteHeader(cw.status)
			_, _ = w.Write(cw.body.Bytes())
			return
		}

		var parsed map[string]any
		if err := json.Unmarshal(cw.body.Bytes(), &parsed); err != nil {
			// Can't parse — pass through unchanged.
			w.WriteHeader(cw.status)
			_, _ = w.Write(cw.body.Bytes())
			return
		}

		result, status, handled := transformByRole(w, route, parsed, selfLink)
		if !handled {
			// A missing-resource error was already written.
			return
		}

		out, err := json.Marshal(result)
		if err != nil {
			w.WriteHeader(cw.status)
			_, _ = w.Write(cw.body.Bytes())
			return
		}

		w.Header().Set("Content-Type", contentTypeJSONAPI)
		w.Header().Del("Content-Length")
		w.WriteHeader(status)
		_, _ = w.Write(out)
	})
}

// transformByRole builds the JSON:API success envelope for a route's Role. It
// returns the transformed document, the HTTP status, and handled=false when it
// has already written a missing-resource 500 to w (CREATE/GET_ONE/UPDATE whose
// handler omitted the declared resource key — the guard: a create can no
// longer silently return meta-only).
func transformByRole(
	w http.ResponseWriter,
	route RouteEntry,
	parsed map[string]any,
	selfLink string,
) (result map[string]any, status int, handled bool) {
	rc := route.resourceConfig()

	switch route.Role {
	case RoleAction:
		// Action endpoints are meta-only — never a data resource.
		return TransformMeta(parsed), http.StatusOK, true

	case RoleList:
		return TransformCollection(parsed, rc, selfLink), http.StatusOK, true

	case RoleCreate:
		// A collection-only bulk create wraps as a collection; the common case is
		// a single created resource → 201 + Location.
		if rc.SingleKey == "" && rc.CollectionKey != "" {
			if !hasKey(parsed, rc.CollectionKey) {
				writeMissingResourceError(w, rc.CollectionKey)
				return nil, 0, false
			}
			return TransformCollection(parsed, rc, selfLink), http.StatusCreated, true
		}
		if !hasKey(parsed, rc.SingleKey) {
			writeMissingResourceError(w, rc.SingleKey)
			return nil, 0, false
		}
		result := TransformSingleResource(parsed, rc, selfLink)
		if data, ok := result["data"].(map[string]any); ok {
			if id, ok := data["id"].(string); ok && id != "" {
				w.Header().Set("Location", selfLink+"/"+id)
			}
		}
		return result, http.StatusCreated, true

	// The single-resource roles are listed so the exhaustive linter sees every declared
	// Role handled; they and an undeclared role share the single-resource read below.
	case RoleGetOne, RoleUpdate, RoleDelete:
	default:
	}

	// Single-resource read: require the declared single resource key. (DELETE is
	// answered with 204 before the transform; it reaches here only if that changes.)
	if !hasKey(parsed, rc.SingleKey) {
		writeMissingResourceError(w, rc.SingleKey)
		return nil, 0, false
	}
	return TransformSingleResource(parsed, rc, selfLink), http.StatusOK, true
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
	if len(body) == 0 {
		return nil, "request body is required and must be a JSON:API document"
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
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
	if typ, _ := data["type"].(string); typ != resourceType {
		return nil, "request data.type must be " + resourceType
	}
	attrsRaw, ok := data["attributes"]
	if !ok {
		return nil, "request data.attributes is required"
	}
	attrs, ok := attrsRaw.(map[string]any)
	if !ok {
		return nil, "request data.attributes must be an object"
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
// request envelope, in the same shape TransformError produces.
func writeRequestError(w http.ResponseWriter, message string) {
	body, err := json.Marshal(map[string]any{
		memberCode: "invalid_argument",
		"message":  message,
	})
	if err != nil {
		body = nil
	}
	w.Header().Set("Content-Type", contentTypeJSONAPI)
	w.Header().Del("Content-Length")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write(TransformError(http.StatusBadRequest, body))
}

// writeMissingResourceError reports a resource response that omitted its declared
// resource as a server error, in the same JSON:API error shape TransformError
// produces for any other 5xx. The key names the protobuf field that was expected,
// which is config data rather than caller input.
func writeMissingResourceError(w http.ResponseWriter, singleKey string) {
	body, err := json.Marshal(map[string]any{
		memberCode: "internal",
		"message":  "response carried no " + singleKey + " resource",
	})
	if err != nil {
		body = nil
	}
	w.Header().Set("Content-Type", contentTypeJSONAPI)
	w.Header().Del("Content-Length")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write(TransformError(http.StatusInternalServerError, body))
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
func (c *captureWriter) Flush() {} // no-op; prevents premature flush

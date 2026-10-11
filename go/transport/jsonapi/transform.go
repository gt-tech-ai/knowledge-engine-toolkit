package jsonapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"
)

// TransformSingleResource converts a Vanguard single-resource response to
// JSON:API format.
//
// Input: {"user": {"id":"123", "email":"a@b.com", ...}} Output: {"data": {"type":"users",
// "id":"123", "attributes":{...}}, "links":{"self":"..."}}
func TransformSingleResource(
	body map[string]any,
	cfg ResourceConfig,
	selfLink string,
) map[string]any {
	raw, ok := body[cfg.SingleKey]
	if !ok {
		// No resource key found — wrap as meta-only response per JSON:API spec.
		// Action endpoints (e.g. InviteMembers) return status info, not resources.
		return TransformMeta(body)
	}

	resource, ok := raw.(map[string]any)
	if !ok {
		return body
	}

	data := buildResourceObject(resource, cfg.Type)

	result := map[string]any{"data": data}
	if selfLink != "" {
		result["links"] = map[string]any{"self": selfLink}
	}

	// Collect remaining response fields (other than the resource key)
	// into "meta" per JSON:API spec. This handles responses like
	// GetCurrentUserResponse that include extra fields alongside the
	// primary resource (e.g. currentOrgId, currentOrgMembership).
	meta := make(map[string]any)
	for k, v := range body {
		if k == cfg.SingleKey {
			continue
		}
		meta[k] = v
	}
	if len(meta) > 0 {
		result["meta"] = meta
	}

	return result
}

// TransformMeta converts a non-resource response to a JSON:API meta-only
// document. Per the JSON:API spec, a valid document must contain at least one
// of: data, errors, or meta. Action endpoints that return status info (not a
// resource) use this format.
//
// Input:  {"sent": 5, "failed": 0, "failedEmails": []}
// Output: {"meta": {"sent": 5, "failed": 0, "failedEmails": []}}
func TransformMeta(body map[string]any) map[string]any {
	meta := make(map[string]any, len(body))
	for k, v := range body {
		meta[k] = v
	}
	return map[string]any{"meta": meta}
}

// TransformCollection converts a Vanguard list response to JSON:API format.
//
// Input: {"users": [...], "pagination": {"nextPageToken":"abc", "totalCount":45,
// "totalIsEstimate":true}} Output: {"data": [...], "meta": {"totalCount":45,
// "totalIsEstimate":true}, "links":{"self":"...", "next":"..."}}
func TransformCollection(
	body map[string]any,
	cfg ResourceConfig,
	selfLink string,
) map[string]any {
	var items []any

	raw, ok := body[cfg.CollectionKey]
	if ok {
		items, ok = raw.([]any)
		if !ok {
			return body
		}
	}
	// If the collection key is missing (protobuf omits empty repeated
	// fields), treat it as an empty array rather than falling back.

	data := make([]any, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			data = append(data, item) // preserve non-object items
			continue
		}
		data = append(data, buildResourceObject(m, cfg.Type))
	}

	result := map[string]any{"data": data}

	// Always add self link when available.
	if selfLink != "" {
		links := map[string]any{"self": selfLink}

		// Map pagination to meta and links.
		if cfg.PaginationKey != "" {
			if pgRaw, ok := body[cfg.PaginationKey]; ok {
				if pg, ok := pgRaw.(map[string]any); ok {
					meta := make(map[string]any)

					if tc, ok := pg["totalCount"]; ok {
						meta["totalCount"] = tc
					}
					// Forward the bounded-count "N+" estimate flag: protojson emits it only
					// when true (non-default bool), so its presence in meta signals the total is capped.
					// Dropping it here silently hid the estimate from every REST client (the UI could
					// never render "10,000+").
					if est, ok := pg["totalIsEstimate"]; ok {
						meta["totalIsEstimate"] = est
					}
					if npt, ok := pg["nextPageToken"]; ok {
						if s, ok := npt.(string); ok && s != "" {
							links["next"] = selfLink + "?page_token=" + s
						}
					}

					if len(meta) > 0 {
						result["meta"] = meta
					}
				}
			}
		}

		result["links"] = links
	}

	return result
}

// buildResourceObject creates a JSON:API resource object from a flat protobuf
// JSON map, pulling "id" to the top level and putting everything else into
// "attributes". Attribute keys are preserved in camelCase to match the
// OpenAPI-generated TypeScript types and JavaScript conventions.
func buildResourceObject(resource map[string]any, resourceType string) map[string]any {
	obj := map[string]any{"type": resourceType}

	if id, ok := resource["id"]; ok {
		obj["id"] = id
	}

	attrs := make(map[string]any, len(resource))
	for k, v := range resource {
		if k == "id" {
			continue
		}
		attrs[k] = v
	}
	// Emitted even when empty: the generated OpenAPI marks `attributes` required on
	// every resource object, so omitting it for an all-default resource would hand
	// the client a document its own generated types say cannot exist.
	obj["attributes"] = attrs

	return obj
}

// TransformError converts a Connect/Vanguard error response body to JSON:API
// error format.
//
// Input:  {"code":5, "message":"user not found"}   (Vanguard: numeric google.rpc.Code)
//
//	or  {"code":"not_found", "message":"user not found"}   (this package's own writers)
//
// Output: {"errors":[{"code":"not_found","status":"404","detail":"user not found"}]}
//
// The member carries three fields: "code" (the machine-readable app error code,
// defaulting to "unknown" when the upstream body is empty, unparseable, or carries
// an unrecognized code), "status" (the HTTP status as a string) and "detail" (the
// human-readable message). Only "detail" is optional — it is omitted rather than
// filled with a placeholder when the upstream body carries no message, so a client
// can tell "no message was sent" from "this is the message". No "title" is emitted:
// it was derived from the status code alone and so duplicated "status".
//
// Vanguard renders a transcoded Connect/gRPC error as a google.rpc.Status whose
// "code" is a NUMBER, so the numeric code is normalized to its canonical connect
// name (see normalizeErrorCode); without that every REST error would degrade to
// "unknown", stripping the machine-readable code every caller keys on.
//
// The document always holds exactly one error member; JSON:API models "errors"
// as an array, and that shape is kept even though nothing here reports more
// than one failure at a time.
func TransformError(statusCode int, body []byte) []byte {
	code := "unknown"
	detail := ""

	// Try to parse the Connect error body.
	if len(body) > 0 {
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err == nil {
			if c := normalizeErrorCode(parsed[memberCode]); c != "" {
				code = c
			}
			if m, ok := parsed["message"].(string); ok && m != "" {
				detail = m
			}
		}
	}

	statusStr := strconv.Itoa(statusCode)
	member := map[string]any{
		memberCode: code,
		"status":   statusStr,
	}
	if detail != "" {
		member["detail"] = detail
	}

	out, err := json.Marshal(map[string]any{"errors": []any{member}})
	if err != nil {
		// Should not happen, but return a safe fallback.
		return []byte(`{"errors":[{"code":"unknown","status":"` + statusStr + `"}]}`)
	}
	return out
}

// memberCode is the error-body member carrying the machine-readable error code,
// both in a Connect error body and in a JSON:API error object.
const memberCode = "code"

// normalizeErrorCode maps the "code" member of an upstream error body to a JSON:API
// machine-readable code string. Two producers reach this: Vanguard renders a
// transcoded Connect/gRPC error as a google.rpc.Status whose code is a NUMBER (the
// google.rpc.Code enum, which connect.Code mirrors 1:1), while this package's own
// error writers pass an already-snake_case string. A string passes through unchanged;
// a number in the valid code range maps to its canonical connect name (5 →
// "not_found", 3 → "invalid_argument", 2 → "unknown", …) — the same vocabulary
// rpc.Sanitize speaks — so REST and Connect callers see identical codes. Any other
// shape (missing, null, out-of-range number) yields "" so TransformError keeps its
// "unknown" default.
func normalizeErrorCode(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case float64: // encoding/json unmarshals every JSON number into float64
		n := int(v)
		if n >= int(connect.CodeCanceled) && n <= int(connect.CodeUnauthenticated) {
			return connect.Code(n).String()
		}
	}
	return ""
}

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

// Package jsonapi provides HTTP middleware that transforms Vanguard REST
// responses into [JSON:API](https://jsonapi.org/format/) envelope format.
//
// Only REST requests are transformed; Connect, gRPC, and gRPC-Web pass
// through unchanged. The middleware sits between the HTTP server and
// Vanguard, rewriting response bodies into {data, meta, links, errors} and
// validating/unwrapping the inbound {data:{type,attributes}} request envelope.
//
// The transformation is driven by an operation-keyed route table the consumer
// supplies (typically generated from its API definitions): each REST RPC
// classifies into a Role, and the middleware transforms by role rather than
// re-guessing method/path/data-presence.
package jsonapi

import "strings"

// Role is the JSON:API operation role a REST route classifies into. The
// consumer's route table (typically emitted by its code generator) tags each
// route with one; the middleware switches on it.
type Role string

// The JSON:API operation roles. Every REST route carries exactly one.
const (
	// RoleList is a collection read: GET returning data[] + meta.
	RoleList Role = "LIST"
	// RoleGetOne is a single-resource read: GET on an instance or a singleton.
	RoleGetOne Role = "GET_ONE"
	// RoleCreate is a resource creation: POST returning the created resource.
	RoleCreate Role = "CREATE"
	// RoleUpdate is a resource replacement/patch: PUT or PATCH.
	RoleUpdate Role = "UPDATE"
	// RoleDelete is a resource deletion: DELETE (204, no body).
	RoleDelete Role = "DELETE"
	// RoleAction is a non-CRUD operation: meta-only, never a data resource.
	RoleAction Role = "ACTION"
)

// RouteEntry is one operation-keyed row of the route table: an HTTP binding
// (method + path template) tagged with its JSON:API Role and the response
// resource keys. It is the single contract the middleware consumes; a
// consumer's route table (hand-written or generated) is a slice of this type.
type RouteEntry struct {
	// Method is the upper-case HTTP verb (GET/POST/PUT/PATCH/DELETE).
	Method string
	// PathTemplate is the raw google.api.http path template, base path included
	// (e.g. "/api/v1/widgets/{widget_id}/parts/{part_id}"). A
	// "{param}" segment is a wildcard when matching a concrete request path.
	PathTemplate string
	// Role is the JSON:API operation role driving the transform.
	Role Role
	// ResourceType is the JSON:API data.type (e.g. "users"); empty for a
	// DELETE/action response that carries no resource shape.
	ResourceType string
	// SingleKey is the response's single-resource key (e.g. "user"); empty when
	// the response carries no single resource.
	SingleKey string
	// CollectionKey is the response's collection key (e.g. "users"); empty when
	// the response carries no collection.
	CollectionKey string
	// PaginationKey is the response's pagination-metadata key; carried for fidelity.
	PaginationKey string
	// RPC is the fully-qualified method name (for diagnostics); unused at runtime.
	RPC string
	// RequestEnvelope is true when the inbound body is JSON:API-enveloped
	// ({data:{type,attributes}}) and must be validated + unwrapped — CREATE and
	// UPDATE only. (Ordered last to keep the string fields packed.)
	RequestEnvelope bool
}

// resourceConfig projects a RouteEntry onto the resource-key shape the transform
// functions consume, keeping transform.go decoupled from the route table.
func (e RouteEntry) resourceConfig() ResourceConfig {
	return ResourceConfig{
		Type:          e.ResourceType,
		SingleKey:     e.SingleKey,
		CollectionKey: e.CollectionKey,
		PaginationKey: e.PaginationKey,
	}
}

// ResourceConfig describes how a single protobuf resource type maps to
// JSON:API response structure. It is the transform functions' input, derived
// from the matched RouteEntry.
type ResourceConfig struct {
	// Type is the JSON:API resource type (e.g. "users").
	Type string

	// SingleKey is the protobuf JSON key for a single resource response
	// (e.g. "user" from GetUserResponse).
	SingleKey string

	// CollectionKey is the protobuf JSON key for a list response
	// (e.g. "users" from ListUsersResponse).
	CollectionKey string

	// PaginationKey is the protobuf JSON key for pagination metadata
	// (e.g. "pagination" from ListUsersResponse).
	PaginationKey string
}

// Config holds the JSON:API middleware configuration: the generated route table
// plus the service base path used only as a fast-reject prefix.
type Config struct {
	// BasePath is the URL prefix all routes share (e.g. "/api/v1/demo"). Paths
	// outside it skip the route-table scan (and pass through untransformed).
	BasePath string

	// Routes is the generated operation-keyed route table for the service.
	Routes []RouteEntry
}

// MatchRoute finds the RouteEntry whose method and path template match a concrete
// request (method, path). A template segment matches when it is a "{param}"
// wildcard or byte-equal to the concrete segment. When more than one template
// could match, a literal-segment match beats a "{param}" wildcard at the same
// position (so "/connectors/test" resolves to the literal action route, not
// "/connectors/{id}"). Returns false when no route matches.
func (c Config) MatchRoute(method, path string) (RouteEntry, bool) {
	if c.BasePath != "" && !strings.HasPrefix(path, c.BasePath) {
		return RouteEntry{}, false
	}

	segs := splitPath(path)

	var best RouteEntry
	var bestLiterals int
	found := false
	// Index-based iteration avoids copying the (large) RouteEntry each loop.
	for i := range c.Routes {
		e := c.Routes[i]
		if !strings.EqualFold(e.Method, method) {
			continue
		}
		literals, ok := matchTemplate(splitPath(e.PathTemplate), segs)
		if !ok {
			continue
		}
		// Prefer the template with the most literal (non-wildcard) segment
		// matches, so a literal route beats a {param} route at the same position.
		if !found || literals > bestLiterals {
			best, bestLiterals, found = e, literals, true
		}
	}
	return best, found
}

// splitPath splits a URL path into its non-empty segments.
func splitPath(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
}

// matchTemplate reports whether a template's segments match a concrete path's
// segments (same length; each template segment is a "{param}" wildcard or
// byte-equal), and returns the count of literal (non-wildcard) matches so the
// caller can prefer the most-literal template on a collision.
func matchTemplate(tmpl, path []string) (literals int, ok bool) {
	if len(tmpl) != len(path) {
		return 0, false
	}
	for i, t := range tmpl {
		if isParam(t) {
			continue
		}
		if t != path[i] {
			return 0, false
		}
		literals++
	}
	return literals, true
}

// isParam reports whether a template segment is a "{name}" parameter capture.
func isParam(seg string) bool {
	return len(seg) >= 2 && seg[0] == '{' && seg[len(seg)-1] == '}'
}

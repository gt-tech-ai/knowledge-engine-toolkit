package openapi

import (
	"fmt"
	"regexp"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Role is the JSON:API operation role a REST RPC is classified into. It is the
// single notion of "what kind of operation is this" that the OpenAPI/TS-client
// generator, the runtime middleware and the completeness gate all consume, so the
// three never re-derive it separately.
type Role string

// The JSON:API operation roles. Every REST RPC classifies into exactly one.
const (
	// RoleList is a collection read: GET returning data[] + pagination.
	RoleList Role = "LIST"
	// RoleGetOne is a single-resource read: GET on a specific instance (trailing
	// {id}) or a singleton (e.g. /me).
	RoleGetOne Role = "GET_ONE"
	// RoleCreate is a resource creation: POST returning the created resource(s)
	// (a single resource, or a collection for a bulk create).
	RoleCreate Role = "CREATE"
	// RoleUpdate is a resource replacement/patch: PUT or PATCH.
	RoleUpdate Role = "UPDATE"
	// RoleDelete is a resource deletion: DELETE (204, no body).
	RoleDelete Role = "DELETE"
	// RoleAction is a non-CRUD operation: meta-only, or a POST whose
	// resource-shaped response is semantically an action (via the marker).
	RoleAction Role = "ACTION"
)

// Binding is the minimal, descriptor-free seam Classify operates over: the HTTP
// binding plus the response's JSON:API resource keys and the optional action
// marker. Keeping Classify pure over this struct (rather than over protoreflect
// descriptors) is what lets the route-table generator and the completeness gate
// share one classifier and unit-test it without a FileDescriptorSet. The
// descriptor → Binding extraction is.
type Binding struct {
	// RPC is the fully-qualified method name (e.g.
	// "library.v1.BookService.GetBook"), used only in error messages.
	RPC string
	// HTTPMethod is the binding's verb (GET/POST/PUT/PATCH/DELETE). Compared
	// case-insensitively.
	HTTPMethod string
	// PathTemplate is the raw google.api.http path template, base path included
	// and no trailing slash (e.g. "/v1/libraries/{library_id}/books").
	PathTemplate string
	// SingleKey is the response's single-resource key from (jsonapi.v1.resource);
	// empty when the response carries no single resource.
	SingleKey string
	// CollectionKey is the response's collection key from (jsonapi.v1.resource);
	// empty when the response carries no collection. SingleKey and CollectionKey
	// both empty means the response carries no resource shape.
	CollectionKey string
	// Action is the (jsonapi.v1.operation){action} marker: it forces a POST with
	// a resource-shaped response to classify as ACTION rather than CREATE.
	Action bool
}

// pathParamSegment matches a single path-template segment that is a bare
// parameter capture, e.g. "{book_id}". It anchors the whole segment and
// forbids inner braces so only a lone "{name}" matches (not a literal segment
// that merely contains braces).
var pathParamSegment = regexp.MustCompile(`^\{[^/{}]+\}$`)

// pathEndsInParam reports whether a path template's last non-empty,
// "/"-delimited segment is a parameter capture (e.g. ".../books/{book_id}"
// is true; ".../books", ".../confirm", ".../me" are false). This is the
// discriminator between an instance operation (GET_ONE) and a collection
// operation, and — for POST — between a create on a collection path and an
// ambiguous specific-instance POST.
func pathEndsInParam(pathTemplate string) bool {
	trimmed := strings.TrimRight(pathTemplate, "/")
	last := trimmed[strings.LastIndex(trimmed, "/")+1:]
	return pathParamSegment.MatchString(last)
}

// Classify derives the JSON:API Role of a REST RPC from its Binding, or returns
// a coded error (apperr.CodeInvalidInput) naming the RPC when the operation is
// unclassifiable — never a silent default. Derivation:
//
//   - DELETE                                              → DELETE
//   - PUT / PATCH                                         → UPDATE
//   - GET + trailing {id}                                 → GET_ONE
//   - GET + collection key                                → LIST
//   - GET + single key (no trailing id, singleton e.g. /me) → GET_ONE
//   - POST + action marker                                → ACTION
//   - POST + resource response + not a trailing-{id} path → CREATE
//   - anything else (a bare POST with no resource and no marker, a POST that
//     returns a resource on a trailing-{id} path without the action marker, or
//     an unhandled method) → error
//
// A POST returning a resource on a specific-instance path is intentionally an
// error: it is ambiguous between an idempotent create and an instance action, so
// the author must add the (jsonapi.v1.operation){action} marker or restructure.
func Classify(b Binding) (Role, error) {
	hasResource := b.SingleKey != "" || b.CollectionKey != ""

	switch strings.ToUpper(b.HTTPMethod) {
	case "DELETE":
		return RoleDelete, nil
	case "PUT", "PATCH":
		return RoleUpdate, nil
	case "GET":
		switch {
		case pathEndsInParam(b.PathTemplate):
			return RoleGetOne, nil
		case b.CollectionKey != "":
			return RoleList, nil
		case b.SingleKey != "":
			return RoleGetOne, nil
		}
	case "POST":
		switch {
		case b.Action:
			return RoleAction, nil
		case hasResource && !pathEndsInParam(b.PathTemplate):
			return RoleCreate, nil
		}
	}

	return "", unclassifiable(b)
}

// unclassifiable builds the coded, RPC-naming error Classify returns when it
// cannot derive a role. The message names the RPC, method, and path so the
// generator/gate failure points at the exact contract to fix.
func unclassifiable(b Binding) error {
	return apperr.InvalidInput(fmt.Sprintf(
		"cannot classify JSON:API role for RPC %q (method=%s path=%s): "+
			"needs a resource response or a (jsonapi.v1.operation){action} marker",
		b.RPC, b.HTTPMethod, b.PathTemplate,
	))
}

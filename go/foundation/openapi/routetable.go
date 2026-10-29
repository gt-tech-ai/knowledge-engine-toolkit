package openapi

import (
	"fmt"
	"go/format"
	"path/filepath"
	"sort"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// RouteEntry is one operation-keyed row of the generated route table: a single
// REST binding (method + path) tagged with the JSON:API Role it classifies into
// and the response's resource keys. It is the single generated contract that
// drives the per-operation OpenAPI envelope, the runtime middleware and the
// completeness gate, so the three never re-derive it separately.
type RouteEntry struct {
	// Method is the binding's HTTP verb (GET/POST/PUT/PATCH/DELETE), upper-cased.
	Method string
	// PathTemplate is the raw google.api.http path template for this binding,
	// base path included (e.g. "/v1/libraries/{library_id}/books").
	PathTemplate string
	// Role is the JSON:API operation role this binding classifies into (see
	// Classify).
	Role Role
	// ResourceType is the response's JSON:API `data.type` value; empty when the
	// response carries no resource.
	ResourceType string
	// SingleKey is the response property holding one resource; empty for
	// collection-only or resource-less responses.
	SingleKey string
	// CollectionKey is the response property holding a resource list; empty for
	// single-only or resource-less responses.
	CollectionKey string
	// PaginationKey is the response property holding pagination metadata;
	// carried for fidelity but inert (the collection envelope's shape is fixed).
	PaginationKey string
	// RPC is the fully-qualified method name (e.g.
	// "library.v1.BookService.GetBook").
	RPC string
	// RequestEnvelope reports whether the operation's request body should be
	// wrapped in the JSON:API {data: {type, attributes}} shape — true only for
	// the CREATE and UPDATE roles.
	RequestEnvelope bool
}

// httpBinding is one google.api.http binding extracted from a method: a verb and
// its path template. A method with additional_bindings yields more than one.
type httpBinding struct {
	// verb is the HTTP method (GET/POST/PUT/PATCH/DELETE).
	verb string
	// path is the google.api.http path template for this binding.
	path string
}

// BuildRouteTable walks every service→method in a marshaled FileDescriptorSet and
// emits, per JSON:API service (grouped by the file's (jsonapi.v1.service) name),
// one RouteEntry for each google.api.http binding. For each binding it assembles a
// Binding from the HTTP verb/path, the response message's (jsonapi.v1.resource)
// keys, and the method's (jsonapi.v1.operation){action} marker, then runs the
// Classify. An unclassifiable, non-exempt RPC hard-fails (propagating
// Classify's coded apperr.CodeInvalidInput) rather than being silently skipped —
// exactly the completeness guarantees. An RPC named in exempt is skipped
// (no entry) when unclassifiable, so a genuine action/internal RPC does not
// fail the generator for the whole surface (the exempt set is config data
// shared with the completeness gate). Files without a (jsonapi.v1.service) option and
// gRPC-only methods (no http binding) are not part of the REST surface and are
// skipped. Each service's entries are sorted by (path, method) for deterministic
// output.
func BuildRouteTable(
	fdsBytes []byte,
	exempt map[string]bool,
) (map[string][]RouteEntry, error) {
	files, types, err := loadDescriptorTypes(fdsBytes)
	if err != nil {
		return nil, err
	}

	// An empty descriptor set (no files) has nothing to route — return an empty
	// table rather than erroring on the absent extension definitions (this is the
	// no-op input, e.g. an orchestration test's stub descriptor).
	if files.NumFiles() == 0 {
		return map[string][]RouteEntry{}, nil
	}

	svcExt, _ := types.FindExtensionByName(jsonAPIServiceExtension)
	resExt, _ := types.FindExtensionByName(jsonAPIResourceExtension)
	opExt, _ := types.FindExtensionByName(jsonAPIOperationExtension)
	if svcExt == nil || resExt == nil || opExt == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"descriptor set is missing the %s / %s / %s extension definitions",
				jsonAPIServiceExtension,
				jsonAPIResourceExtension,
				jsonAPIOperationExtension,
			),
		)
	}

	table := map[string][]RouteEntry{}
	var walkErr error

	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		svc, ok, err := readServiceOption(fd, svcExt, types)
		if err != nil {
			walkErr = apperr.Wrap(err, apperr.CodeInternal, fd.Path())
			return false
		}
		if !ok {
			return true // not part of the JSON:API REST surface
		}

		svcs := fd.Services()
		for i := range svcs.Len() {
			methods := svcs.Get(i).Methods()
			for j := range methods.Len() {
				entries, err := routeEntriesForMethod(
					methods.Get(j), resExt, opExt, types, exempt,
				)
				if err != nil {
					walkErr = err
					return false
				}
				table[svc.name] = append(table[svc.name], entries...)
			}
		}
		return true
	})
	if walkErr != nil {
		return nil, walkErr
	}

	for svc := range table {
		sortRouteEntries(table[svc])
	}
	return table, nil
}

// routeEntriesForMethod classifies one method's HTTP bindings into RouteEntries.
// It returns nil (no entries, no error) for a gRPC-only method. An unclassifiable
// binding whose RPC is exempt is skipped; otherwise Classify's coded error is
// propagated.
func routeEntriesForMethod(
	md protoreflect.MethodDescriptor,
	resExt, opExt protoreflect.ExtensionType,
	types *protoregistry.Types,
	exempt map[string]bool,
) ([]RouteEntry, error) {
	bindings, err := methodHTTPBindings(md)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, nil // gRPC-only method — not a REST route
	}

	rpc := string(md.FullName())

	action, err := readOperationOption(md, opExt, types)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, rpc)
	}

	rc, hasRes, err := readResourceOption(md.Output(), resExt, types)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, rpc)
	}
	if hasRes && rc.resourceType == "" {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"%s: response %q: (jsonapi.v1.resource) is missing required field 'type'",
				rpc, md.Output().Name(),
			),
		)
	}

	var out []RouteEntry
	for _, b := range bindings {
		role, err := Classify(Binding{
			RPC:           rpc,
			HTTPMethod:    b.verb,
			PathTemplate:  b.path,
			SingleKey:     rc.singleKey,
			CollectionKey: rc.collectionKey,
			Action:        action,
		})
		if err != nil {
			if exempt[rpc] {
				continue // a genuine action/internal RPC — skipped, not fatal
			}
			return nil, err
		}
		out = append(out, RouteEntry{
			Method:          strings.ToUpper(b.verb),
			PathTemplate:    b.path,
			Role:            role,
			ResourceType:    rc.resourceType,
			SingleKey:       rc.singleKey,
			CollectionKey:   rc.collectionKey,
			PaginationKey:   rc.paginationKey,
			RequestEnvelope: role == RoleCreate || role == RoleUpdate,
			RPC:             rpc,
		})
	}
	return out, nil
}

// methodHTTPBindings extracts every google.api.http binding on a method (the
// primary pattern plus each additional_binding), so a method with more than one
// route can't be silently envelope-less. A method with no http option is
// gRPC-only and yields no bindings. A `custom` (AIP colon-method) binding is an
// error rather than a silent mis-verbing — the repo has none today, and this
// keeps a future one from slipping through unclassified.
func methodHTTPBindings(md protoreflect.MethodDescriptor) ([]httpBinding, error) {
	opts := md.Options()
	if opts == nil || !proto.HasExtension(opts, annotations.E_Http) {
		return nil, nil
	}
	rule, ok := proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule)
	if !ok || rule == nil {
		return nil, nil
	}

	rpc := string(md.FullName())
	rules := append([]*annotations.HttpRule{rule}, rule.GetAdditionalBindings()...)
	out := make([]httpBinding, 0, len(rules))
	for _, r := range rules {
		b, err := bindingFromRule(rpc, r)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// bindingFromRule maps a single HttpRule's verb+path oneof to an httpBinding,
// erroring on a `custom` binding or a pattern-less rule.
func bindingFromRule(rpc string, r *annotations.HttpRule) (httpBinding, error) {
	switch p := r.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		return httpBinding{verb: "GET", path: p.Get}, nil
	case *annotations.HttpRule_Put:
		return httpBinding{verb: "PUT", path: p.Put}, nil
	case *annotations.HttpRule_Post:
		return httpBinding{verb: "POST", path: p.Post}, nil
	case *annotations.HttpRule_Patch:
		return httpBinding{verb: "PATCH", path: p.Patch}, nil
	case *annotations.HttpRule_Delete:
		return httpBinding{verb: "DELETE", path: p.Delete}, nil
	case *annotations.HttpRule_Custom:
		return httpBinding{}, apperr.InvalidInput(fmt.Sprintf(
			"RPC %q uses a custom (colon-method) http binding, which the route table does not "+
				"support",
			rpc,
		))
	default:
		return httpBinding{}, apperr.InvalidInput(fmt.Sprintf(
			"RPC %q has a google.api.http rule with no verb pattern", rpc,
		))
	}
}

// WriteRuntimeRouteTable writes a generated Go file per service
// (<outDir>/<service>/routes.go) holding the routes' shared `BasePath` and a
// `var Routes []jsonapi.RouteEntry` literal — the runtime route table the JSON:API
// middleware consumes. runtimeImport is the import path of the runtime jsonapi package
// that declares RouteEntry (a parameter so this generator stays repo-agnostic). Each file
// is gofmt-formatted; a malformed emission fails loudly rather than writing
// invalid Go. Output is deterministic (services and entries are already sorted). The
// files are written through fsys.
func WriteRuntimeRouteTable(
	fsys interfaces.FS,
	routes map[string][]RouteEntry,
	outDir, runtimeImport string,
) error {
	services := make([]string, 0, len(routes))
	for svc := range routes {
		services = append(services, svc)
	}
	sort.Strings(services)

	for _, svc := range services {
		var b strings.Builder
		b.WriteString(
			"// Code generated by the knowledge-engine openapi route table; DO NOT EDIT.\n\n",
		)
		fmt.Fprintf(&b, "package %s\n\n", goPackageName(svc))
		fmt.Fprintf(&b, "import jsonapi %q\n\n", runtimeImport)
		fmt.Fprintf(
			&b,
			"// BasePath is the URL prefix every route of the %q service shares.\n"+
				"const BasePath = %q\n\n",
			svc,
			commonPathPrefix(routes[svc]),
		)
		fmt.Fprintf(
			&b,
			"// Routes is the generated JSON:API route table for the %q service.\nvar Routes = "+
				"[]jsonapi.RouteEntry{\n",
			svc,
		)
		for i := range routes[svc] {
			e := &routes[svc][i]
			fmt.Fprintf(
				&b,
				"\t{Method: %q, PathTemplate: %q, Role: %q, ResourceType: %q, SingleKey: %q, "+
					"CollectionKey: %q, PaginationKey: %q, RequestEnvelope: %t, RPC: %q},\n",
				e.Method,
				e.PathTemplate,
				string(e.Role),
				e.ResourceType,
				e.SingleKey,
				e.CollectionKey,
				e.PaginationKey,
				e.RequestEnvelope,
				e.RPC,
			)
		}
		b.WriteString("}\n")

		formatted, err := format.Source([]byte(b.String()))
		if err != nil {
			return apperr.Wrap(
				err,
				apperr.CodeInternal,
				fmt.Sprintf("format generated routes for service %q", svc),
			)
		}

		dir := filepath.Join(outDir, svc)
		if err := fsys.MkdirAll(dir, 0o755); err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "create "+dir)
		}
		if err := fsys.WriteFile(
			filepath.Join(dir, "routes.go"),
			formatted,
			0o600,
		); err != nil {
			return apperr.Wrap(
				err,
				apperr.CodeInternal,
				fmt.Sprintf("write routes.go for service %q", svc),
			)
		}
	}
	return nil
}

// goPackageName sanitizes a service name into a valid lower-case Go package
// identifier (letters/digits only, leading digit prefixed with an underscore).
func goPackageName(service string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(service) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" {
		return "routes"
	}
	if name[0] >= '0' && name[0] <= '9' {
		return "_" + name
	}
	return name
}

// sortRouteEntries orders entries by (PathTemplate, Method) in place, so the
// emitted route table is deterministic regardless of file/method walk order.
func sortRouteEntries(entries []RouteEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].PathTemplate != entries[j].PathTemplate {
			return entries[i].PathTemplate < entries[j].PathTemplate
		}
		if entries[i].Method != entries[j].Method {
			return entries[i].Method < entries[j].Method
		}
		// RPC tiebreaker: total order so the emitted table is deterministic even
		// if two RPCs bind the same (path, method).
		return entries[i].RPC < entries[j].RPC
	})
}

// commonPathPrefix is the longest run of whole path segments every route's template
// starts with ("/v1/shop" for /v1/shop/orders and /v1/shop/carts), the
// JSON:API middleware's base path; "" when the routes share none.
func commonPathPrefix(routes []RouteEntry) string {
	if len(routes) == 0 {
		return ""
	}
	prefix := strings.Split(strings.Trim(routes[0].PathTemplate, "/"), "/")
	for i := range routes[1:] {
		segs := strings.Split(strings.Trim(routes[i+1].PathTemplate, "/"), "/")
		n := 0
		for n < len(prefix) && n < len(segs) && prefix[n] == segs[n] {
			n++
		}
		prefix = prefix[:n]
	}
	// A trailing {param} is per-request, never part of the base.
	for len(prefix) > 0 && strings.HasPrefix(prefix[len(prefix)-1], "{") {
		prefix = prefix[:len(prefix)-1]
	}
	if len(prefix) == 0 {
		return ""
	}
	return "/" + strings.Join(prefix, "/")
}

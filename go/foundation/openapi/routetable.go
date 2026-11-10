package openapi

import (
	"cmp"
	"fmt"
	"go/format"
	"path/filepath"
	"slices"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// publicDirMode is the mode of a directory anyone may read.
const publicDirMode = 0o755

// privateFileMode is the mode of a file only its owner reads.
const privateFileMode = 0o600

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

	exts, err := resolveJSONAPIExtensions(types)
	if err != nil {
		return nil, err
	}

	table := map[string][]RouteEntry{}

	err = exts.walkSurface(
		files,
		func(svc serviceOpt, md protoreflect.MethodDescriptor) error {
			entries, opErr := routeEntriesForMethod(md, exts, exempt)
			if opErr != nil {
				return opErr
			}

			table[svc.name] = append(table[svc.name], entries...)

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	for svc := range table {
		sortRouteEntries(table[svc])
	}

	return table, nil
}

// jsonAPIExtensions holds the jsonapi.v1 extensions resolved from a descriptor
// set's own definitions, shared by the route table and the completeness gate so
// the two read the identical surface.
type jsonAPIExtensions struct {
	// svc is the (jsonapi.v1.service) file option.
	svc protoreflect.ExtensionType

	// res is the (jsonapi.v1.resource) message option.
	res protoreflect.ExtensionType

	// op is the (jsonapi.v1.operation) method option.
	op protoreflect.ExtensionType

	// types resolves extension values during option decoding.
	types *protoregistry.Types
}

// resolveJSONAPIExtensions finds the three jsonapi.v1 extensions, failing loudly
// when the descriptor set does not define them.
func resolveJSONAPIExtensions(types *protoregistry.Types) (*jsonAPIExtensions, error) {
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

	return &jsonAPIExtensions{svc: svcExt, res: resExt, op: opExt, types: types}, nil
}

// surfaceVisitor receives each method of the JSON:API REST surface.
type surfaceVisitor func(svc serviceOpt, md protoreflect.MethodDescriptor) error

// walkSurface visits every method of every (jsonapi.v1.service) file, stopping at
// the first error.
func (x *jsonAPIExtensions) walkSurface(
	files *protoregistry.Files,
	visit surfaceVisitor,
) error {
	var walkErr error

	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		walkErr = x.visitFile(fd, visit)

		return walkErr == nil
	})

	return walkErr
}

// visitFile visits each service of fd, skipping a file outside the REST surface.
func (x *jsonAPIExtensions) visitFile(
	fileDescriptor protoreflect.FileDescriptor,
	visit surfaceVisitor,
) error {
	svc, ok, err := readServiceOption(fileDescriptor, x.svc, x.types)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, fileDescriptor.Path())
	}

	if !ok {
		return nil // not part of the JSON:API REST surface
	}

	services := fileDescriptor.Services()
	for i := range services.Len() {
		err := visitServiceMethods(services.Get(i), svc, visit)
		if err != nil {
			return err
		}
	}

	return nil
}

// visitServiceMethods visits each method of one service, stopping at the first error.
func visitServiceMethods(
	sd protoreflect.ServiceDescriptor, svc serviceOpt, visit surfaceVisitor,
) error {
	methods := sd.Methods()
	for i := range methods.Len() {
		err := visit(svc, methods.Get(i))
		if err != nil {
			return err
		}
	}

	return nil
}

// methodRoutes is one method's decoded annotations and HTTP bindings; a gRPC-only
// method has no bindings.
type methodRoutes struct {
	// rpc is the method's full name.
	rpc string

	// resource is the decoded (jsonapi.v1.resource) of the response.
	resource resourceConfig

	// bindings is every google.api.http binding on the method.
	bindings []httpBinding

	// action reports the (jsonapi.v1.operation){action} marker.
	action bool
}

// binding builds the Classify input for one of the method's HTTP bindings.
func (m *methodRoutes) binding(b httpBinding) Binding {
	return Binding{
		RPC:           m.rpc,
		HTTPMethod:    b.verb,
		PathTemplate:  b.path,
		SingleKey:     m.resource.singleKey,
		CollectionKey: m.resource.collectionKey,
		Action:        m.action,
	}
}

// entry builds the RouteEntry of one classified HTTP binding.
func (m *methodRoutes) entry(b httpBinding, role Role) RouteEntry {
	return RouteEntry{
		Method:          strings.ToUpper(b.verb),
		PathTemplate:    b.path,
		Role:            role,
		ResourceType:    m.resource.resourceType,
		SingleKey:       m.resource.singleKey,
		CollectionKey:   m.resource.collectionKey,
		PaginationKey:   m.resource.paginationKey,
		RequestEnvelope: role == RoleCreate || role == RoleUpdate,
		RPC:             m.rpc,
	}
}

// methodRoutes decodes md's annotations. A malformed annotation or a type-less
// resource is an error; a gRPC-only method returns no bindings and no error.
func (x *jsonAPIExtensions) methodRoutes(
	methodDescriptor protoreflect.MethodDescriptor,
) (*methodRoutes, error) {
	bindings, err := methodHTTPBindings(methodDescriptor)
	if err != nil {
		return nil, err
	}

	routes := &methodRoutes{
		rpc:      string(methodDescriptor.FullName()),
		bindings: bindings,
		resource: stdx.Zero[resourceConfig](),
		action:   false,
	}
	if len(bindings) == 0 {
		return routes, nil // gRPC-only method — not a REST route
	}

	routes.action, err = readOperationOption(methodDescriptor, x.op, x.types)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, routes.rpc)
	}

	secondResourceConfig, hasRes, err := readResourceOption(
		methodDescriptor.Output(),
		x.res,
		x.types,
	)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, routes.rpc)
	}

	if hasRes && secondResourceConfig.resourceType == "" {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"%s: response %q: (jsonapi.v1.resource) is missing required field 'type'",
				routes.rpc, methodDescriptor.Output().Name(),
			),
		)
	}

	routes.resource = secondResourceConfig

	return routes, nil
}

// routeEntriesForMethod classifies one method's HTTP bindings into RouteEntries.
// It returns nil (no entries, no error) for a gRPC-only method. An unclassifiable
// binding whose RPC is exempt is skipped; otherwise Classify's coded error is
// propagated.
func routeEntriesForMethod(
	md protoreflect.MethodDescriptor,
	exts *jsonAPIExtensions,
	exempt map[string]bool,
) ([]RouteEntry, error) {
	routes, err := exts.methodRoutes(md)
	if err != nil {
		return nil, err
	}

	var out []RouteEntry

	for _, secondHTTPBinding := range routes.bindings {
		role, err := Classify(new(routes.binding(secondHTTPBinding)))
		if err != nil && exempt[routes.rpc] {
			continue // a genuine action/internal RPC — skipped, not fatal
		}

		if err != nil {
			return nil, err
		}

		out = append(out, routes.entry(secondHTTPBinding, role))
	}

	return out, nil
}

// methodHTTPBindings extracts every google.api.http binding on a method (the
// primary pattern plus each additional_binding), so a method with more than one
// route can't be silently envelope-less. A method with no http option is
// gRPC-only and yields no bindings. A `custom` (AIP colon-method) binding is an
// error rather than a silent mis-verbing — the repo has none today, and this
// keeps a future one from slipping through unclassified.
func methodHTTPBindings(
	methodDescriptor protoreflect.MethodDescriptor,
) ([]httpBinding, error) {
	opts := methodDescriptor.Options()
	if opts == nil || !proto.HasExtension(opts, annotations.E_Http) {
		return nil, nil
	}

	rule, ok := proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule)
	if !ok || rule == nil {
		return nil, nil
	}

	rpc := string(methodDescriptor.FullName())

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
	switch httpPattern := r.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		return httpBinding{verb: "GET", path: httpPattern.Get}, nil
	case *annotations.HttpRule_Put:
		return httpBinding{verb: "PUT", path: httpPattern.Put}, nil
	case *annotations.HttpRule_Post:
		return httpBinding{verb: "POST", path: httpPattern.Post}, nil
	case *annotations.HttpRule_Patch:
		return httpBinding{verb: "PATCH", path: httpPattern.Patch}, nil
	case *annotations.HttpRule_Delete:
		return httpBinding{verb: "DELETE", path: httpPattern.Delete}, nil
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

	slices.Sort(services)

	for _, svc := range services {
		formatted, err := renderRouteFile(svc, routes[svc], runtimeImport)
		if err != nil {
			return err
		}

		err = writeRouteFile(fsys, outDir, svc, formatted)
		if err != nil {
			return err
		}
	}

	return nil
}

// renderRouteFile emits and gofmts one service's generated routes.go; a malformed
// emission fails here rather than reaching the disk.
func renderRouteFile(
	svc string,
	entries []RouteEntry,
	runtimeImport string,
) ([]byte, error) {
	var builder strings.Builder

	_, _ = builder.WriteString(
		"// Code generated by the knowledge-engine openapi route table; DO NOT EDIT.\n\n",
	)
	fmt.Fprintf(&builder, "package %s\n\n", goPackageName(svc))
	fmt.Fprintf(&builder, "import jsonapi %q\n\n", runtimeImport)
	fmt.Fprintf(
		&builder,
		"// BasePath is the URL prefix every route of the %q service shares.\n"+
			"const BasePath = %q\n\n",
		svc,
		commonPathPrefix(entries),
	)
	fmt.Fprintf(
		&builder,
		"// Routes is the generated JSON:API route table for the %q service.\nvar Routes = "+
			"[]jsonapi.RouteEntry{\n",
		svc,
	)

	for i := range entries {
		routeEntry := &entries[i]
		fmt.Fprintf(
			&builder,
			"\t{Method: %q, PathTemplate: %q, Role: %q, ResourceType: %q, SingleKey: %q, "+
				"CollectionKey: %q, PaginationKey: %q, RequestEnvelope: %t, RPC: %q},\n",
			routeEntry.Method,
			routeEntry.PathTemplate,
			string(routeEntry.Role),
			routeEntry.ResourceType,
			routeEntry.SingleKey,
			routeEntry.CollectionKey,
			routeEntry.PaginationKey,
			routeEntry.RequestEnvelope,
			routeEntry.RPC,
		)
	}

	_, _ = builder.WriteString("}\n")

	formatted, err := format.Source([]byte(builder.String()))
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("format generated routes for service %q", svc),
		)
	}

	return formatted, nil
}

// writeRouteFile stores one service's rendered routes.go under outDir through fsys.
func writeRouteFile(fsys interfaces.FS, outDir, svc string, formatted []byte) error {
	dir := filepath.Join(outDir, svc)

	err := fsys.MkdirAll(dir, publicDirMode)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "create "+dir)
	}

	err = fsys.WriteFile(filepath.Join(dir, "routes.go"), formatted, privateFileMode)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("write routes.go for service %q", svc),
		)
	}

	return nil
}

// goPackageName sanitizes a service name into a valid lower-case Go package
// identifier (letters/digits only, leading digit prefixed with an underscore).
func goPackageName(service string) string {
	var builder strings.Builder

	for _, r := range strings.ToLower(service) {
		if isASCIIDigit(r) || (r >= 'a' && r <= 'z') {
			_, _ = builder.WriteRune(r)
		}
	}

	name := builder.String()
	if name == "" {
		return "routes"
	}

	if isASCIIDigit(rune(name[0])) {
		return "_" + name
	}

	return name
}

// isASCIIDigit reports whether r is 0-9; unicode.IsDigit also accepts digits a Go
// package name cannot start from.
func isASCIIDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

// sortRouteEntries orders entries by (PathTemplate, Method) in place, so the
// emitted route table is deterministic regardless of file/method walk order.
func sortRouteEntries(entries []RouteEntry) {
	slices.SortFunc(entries, func(currentRouteEntry, routeEntry RouteEntry) int {
		// RPC tiebreaker: total order so the emitted table is deterministic even
		// if two RPCs bind the same (path, method).
		return cmp.Or(
			cmp.Compare(currentRouteEntry.PathTemplate, routeEntry.PathTemplate),
			cmp.Compare(currentRouteEntry.Method, routeEntry.Method),
			cmp.Compare(currentRouteEntry.RPC, routeEntry.RPC),
		)
	})
}

// commonPathPrefix is the longest run of whole path segments every route's template
// starts with ("/v1/shop" for /v1/shop/orders and /v1/shop/carts), the
// JSON:API middleware's base path; "" when the routes share none.
func commonPathPrefix(routes []RouteEntry) string {
	if len(routes) == 0 {
		return ""
	}

	prefix := pathSegments(routes[0].PathTemplate)
	for i := range routes[1:] {
		prefix = prefix[:sharedPrefixLen(prefix, pathSegments(routes[i+1].PathTemplate))]
	}

	// A trailing {param} is per-request, never part of the base.
	for len(prefix) > 0 && strings.HasPrefix(prefix[len(prefix)-1], "{") {
		prefix = prefix[:len(prefix)-1]
	}

	if len(prefix) == 0 {
		return ""
	}

	return textSlash + strings.Join(prefix, textSlash)
}

// pathSegments splits a path template into its segments, ignoring outer slashes.
func pathSegments(template string) []string {
	return strings.Split(strings.Trim(template, textSlash), textSlash)
}

// sharedPrefixLen counts the leading segments a and b have in common.
func sharedPrefixLen(a, b []string) int {
	count := 0

	for count < len(a) && count < len(b) && a[count] == b[count] {
		count++
	}

	return count
}

package unit_test

// Black-box tests for the openapi codegen package's operation-keyed route table They
// drive the exported openapi.BuildRouteTable over a self-contained, hand-authored
// FileDescriptorSet — one that carries a synthetic jsonapi/v1/annotations.proto (the
// three extends) plus google/api/http so the jsonapi extensions resolve dynamically
// exactly as they do for a real `buf build` descriptor, WITHOUT importing the generated
// jsonapi Go stubs (which are gitignored and absent before proto generation). This keeps
// the whole unit_test package buildable on a fresh checkout.

import (
	"cmp"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/fsys"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/openapi"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// ── synthetic-FDS builder (no generated Go, no buf) ──────────────────────────

// sortedEntries returns a service's route entries sorted by (PathTemplate,
// Method) for a stable comparison.
func sortedEntries(entries []openapi.RouteEntry) []openapi.RouteEntry {
	out := append([]openapi.RouteEntry(nil), entries...)
	slices.SortFunc(out, func(a, b openapi.RouteEntry) int {
		return cmp.Or(
			cmp.Compare(a.PathTemplate, b.PathTemplate),
			cmp.Compare(a.Method, b.Method),
		)
	})

	return out
}

// docMessages/docMethods are the synthetic test.v1 surface shared by the
// per-operation-envelope Convert tests: a documents resource with LIST / GET_ONE /
// CREATE / UPDATE and a resource-shaped POST action (Confirm).
func docMessages(s *fixtures.JSONAPISetter) []*descriptorpb.DescriptorProto {
	return []*descriptorpb.DescriptorProto{
		fixtures.ResourceMessage(s, textDocumentTitle, textDocuments, textDocument, ""),
		fixtures.ResourceMessage(s, textDocumentlist, textDocuments, "", textDocuments),
		fixtures.EmptyMessage(textEmptyTitle),
		fixtures.EmptyMessage("CreateDocumentRequest"),
		fixtures.EmptyMessage("UpdateDocumentRequest"),
	}
}

func docMethods(
	jsonapiSetter *fixtures.JSONAPISetter,
) []*descriptorpb.MethodDescriptorProto {
	return []*descriptorpb.MethodDescriptorProto{
		fixtures.Method("ListDocuments", textEmptyTitle, textDocumentlist,
			jsonapiSetter.MethodOpts(textGetUpper, textAPIV1Documents)),
		fixtures.Method(textGetdocument, textEmptyTitle, textDocumentTitle,
			jsonapiSetter.MethodOpts(textGetUpper, textAPIV1DocumentsDocument)),
		fixtures.Method("CreateDocument", "CreateDocumentRequest", textDocumentTitle,
			jsonapiSetter.MethodOpts(textPost, textAPIV1Documents)),
		fixtures.Method("UpdateDocument", "UpdateDocumentRequest", textDocumentTitle,
			jsonapiSetter.MethodOpts(textPatch, textAPIV1DocumentsDocument)),
		fixtures.Method("ConfirmDocument", textEmptyTitle, textDocumentTitle,
			jsonapiSetter.ActionMethodOpts(textPost, textAPIV1DocumentsDocumentPath)),
	}
}

// docSwagger is a Swagger 2.0 spec for the documents surface (full google.api.http
// path keys), the input protoc-gen-openapiv2 would produce for docMethods.
const docSwagger = `{
  "swagger": "2.0",
  "info": {"title": "testsvc", "version": "1.0"},
  "paths": {
    "/api/v1/documents": {
      "get": {"operationId": "ListDocuments",
        "responses": {"200": {"description": "ok",
          "schema": {"$ref": "#/definitions/ListDocumentsResponse"}}}},
      "post": {"operationId": "CreateDocument",
        "parameters": [{"name": "body", "in": "body", "required": true,
          "schema": {"$ref": "#/definitions/CreateDocumentRequest"}}],
        "responses": {"200": {"description": "created",
          "schema": {"$ref": "#/definitions/GetDocumentResponse"}}}}
    },
    "/api/v1/documents/{document_id}": {
      "get": {"operationId": "GetDocument",
        "parameters": [{"name": "document_id", "in": "path", "required": true,
          "type": "string"}],
        "responses": {"200": {"description": "ok",
          "schema": {"$ref": "#/definitions/GetDocumentResponse"}}}},
      "patch": {"operationId": "UpdateDocument",
        "parameters": [{"name": "body", "in": "body", "required": true,
          "schema": {"$ref": "#/definitions/UpdateDocumentRequest"}}],
        "responses": {"200": {"description": "ok",
          "schema": {"$ref": "#/definitions/GetDocumentResponse"}}}}
    },
    "/api/v1/documents/{document_id}/confirm": {
      "post": {"operationId": "ConfirmDocument",
        "responses": {"200": {"description": "ok",
          "schema": {"$ref": "#/definitions/ConfirmResponse"}}}}
    }
  },
  "definitions": {
    "Document": {"type": "object", "properties": {"id": {"type": "string"},
      "title": {"type": "string"}}, "required": ["id"]},
    "GetDocumentResponse": {"type": "object",
      "properties": {"document": {"$ref": "#/definitions/Document"}}},
    "ListDocumentsResponse": {"type": "object",
      "properties": {"documents": {"type": "array",
      "items": {"$ref": "#/definitions/Document"}}}},
    "ConfirmResponse": {"type": "object",
      "properties": {"document": {"$ref": "#/definitions/Document"},
      "status": {"type": "string"}}},
    "CreateDocumentRequest": {"type": "object",
      "properties": {"title": {"type": "string"}, "body": {"type": "string"}}},
    "UpdateDocumentRequest": {"type": "object",
      "properties": {"title": {"type": "string"}}}
  }
}`

// docSchemeName names the API-key security scheme the synthetic swagger declares.
const docSchemeName = "apiKeyHeader"

// docHeaders is the API-key security scheme runDocConvert declares: one header every
// operation requires.
func docHeaders() map[string]string {
	return map[string]string{docSchemeName: "X-API-Key"}
}

// runDocConvert writes the synthetic descriptor + a swagger file, builds the route
// table, and runs openapi.Convert over them, returning the parsed output spec.
func runDocConvert(t *testing.T, swagger string) map[string]any {
	t.Helper()

	s := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	fds := fixtures.BuildRouteFDS(t, s.FileOptsWithService(),
		docMessages(&s), docMethods(&s))

	descPath := filepath.Join(t.TempDir(), textDBinpb)
	require.NoError(t, os.WriteFile(descPath, fds, writeFileInput))

	routes, err := openapi.BuildRouteTable(fds, nil)
	require.NoError(t, err)

	inDir := filepath.Join(t.TempDir(), textIn)
	require.NoError(
		t,
		os.MkdirAll(filepath.Join(inDir, textTest, textV1), mkdirAllInputConvert),
	)
	require.NoError(t, os.WriteFile(
		filepath.Join(
			inDir,
			textTest,
			textV1,
			textTestSwaggerJSON,
		),
		[]byte(swagger),
		writeFileInput,
	))

	res, err := openapi.Convert(
		fsys.NewOSFS(),
		inDir,
		filepath.Join(t.TempDir(), textOut),
		descPath,
		routes, docHeaders(),
	)
	require.NoError(t, err)
	require.Len(t, res.Written, 1)

	data, err := os.ReadFile(res.Written[0])
	require.NoError(t, err)

	var spec map[string]any

	require.NoError(t, json.Unmarshal(data, &spec))

	return spec
}

// successSchema returns the application/vnd.api+json 200 response schema for
// paths[path][method].
func successSchema(
	t *testing.T,
	spec map[string]any,
	path, method string,
) map[string]any {
	t.Helper()

	paths := openapiAsMap(t, spec[textPaths], textPaths)
	op := openapiAsMap(t, openapiAsMap(t, paths[path], path)[method], method)
	resp := openapiAsMap(
		t,
		openapiAsMap(t, op["responses"], "responses")[text200],
		text200,
	)
	content := openapiAsMap(t, resp["content"], "content")

	return openapiAsMap(t,
		openapiAsMap(t, content["application/vnd.api+json"], "aj")["schema"], "schema")
}

// ── tests ────────────────────────────────────────────────────────────────────

// TestRouteTable_EmitsEntryPerHTTPRPC tests that BuildRouteTable walks
// services→methods and emits one operation-keyed RouteEntry per google.api.http
// binding, classifying every REST role and honoring additional_bindings.
//
// Why this test is important:
//   - The route table is the single generated contract that drives the OpenAPI
//     envelope, the runtime middleware, and the completeness gate; a missed
//     binding or a mis-derived role silently reshapes (or un-envelopes) an
//
// endpoint — the exact drift exists to remove.
//
// What it tests:
//   - LIST/GET_ONE/CREATE/UPDATE/DELETE/ACTION each derive from their HTTP verb +
//     path + response resource keys + the action marker;
//   - RequestEnvelope is true only for CREATE/UPDATE;
//   - a method with additional_bindings emits one entry per binding;
//   - a gRPC-only method (no http binding) is skipped.
func TestRouteTable_EmitsEntryPerHTTPRPC(t *testing.T) {
	t.Parallel()

	jsonapiSetter := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))

	messages := []*descriptorpb.DescriptorProto{
		fixtures.ResourceMessage(
			&jsonapiSetter,
			textDocumentTitle,
			textDocuments,
			textDocument,
			"",
		),
		fixtures.ResourceMessage(
			&jsonapiSetter,
			textDocumentlist,
			textDocuments,
			"",
			textDocuments,
		),
		fixtures.EmptyMessage(textEmptyTitle),
	}
	methods := []*descriptorpb.MethodDescriptorProto{
		fixtures.Method("ListDocuments", textEmptyTitle, textDocumentlist,
			jsonapiSetter.MethodOpts(textGetUpper, textAPIV1Documents)),
		fixtures.Method(textGetdocument, textEmptyTitle, textDocumentTitle,
			jsonapiSetter.MethodOpts(textGetUpper, textAPIV1DocumentsDocument)),
		fixtures.Method("CreateDocument", textDocumentTitle, textDocumentTitle,
			jsonapiSetter.MethodOpts(textPost, textAPIV1Documents)),
		fixtures.Method("UpdateDocument", textDocumentTitle, textDocumentTitle,
			jsonapiSetter.MethodOpts(textPatch, textAPIV1DocumentsDocument)),
		fixtures.Method("DeleteDocument", textEmptyTitle, textEmptyTitle,
			jsonapiSetter.MethodOpts(textDeleteUpper, textAPIV1DocumentsDocument)),
		fixtures.Method("ConfirmDocument", textEmptyTitle, textDocumentTitle,
			jsonapiSetter.ActionMethodOpts(textPost, textAPIV1DocumentsDocumentPath)),
		fixtures.Method("GetDocumentAliased", textEmptyTitle, textDocumentTitle,
			jsonapiSetter.MethodOpts(textGetUpper, "/api/v1/aliases/{alias}",
				[2]string{textGetUpper, "/api/v1/aliases/by-name/{name}"})),
		fixtures.Method(
			"InternalPing",
			textEmptyTitle,
			textEmptyTitle,
			nil,
		), // gRPC-only → skipped
	}

	fds := fixtures.BuildRouteFDS(
		t,
		jsonapiSetter.FileOptsWithService(),
		messages,
		methods,
	)

	table, err := openapi.BuildRouteTable(fds, nil)
	require.NoError(t, err)

	want := testRouteTableEmitsEntryPerHTTPRPCRows()

	require.Len(t, table, 1, "one service bucket")
	assert.Equal(t, want, sortedEntries(table["testsvc"]))
}

// testRouteTableEmitsEntryPerHTTPRPCRows builds the table
// testRouteTableEmitsEntryPerHTTPRPC runs.
func testRouteTableEmitsEntryPerHTTPRPCRows() []openapi.RouteEntry {
	return slices.Concat(
		testRouteTableEmitsEntryPerHTTPRPCAliasRows(),
		testRouteTableEmitsEntryPerHTTPRPCDocumentRows(),
	)
}

// testRouteTableEmitsEntryPerHTTPRPCAliasRows lists part of the
// testRouteTableEmitsEntryPerHTTPRPCRows rows.
func testRouteTableEmitsEntryPerHTTPRPCAliasRows() []openapi.RouteEntry {
	return []openapi.RouteEntry{
		fixtures.Build(func(row *openapi.RouteEntry) {
			row.Method = textGetUpper
			row.PathTemplate = "/api/v1/aliases/by-name/{name}"
			row.Role = openapi.RoleGetOne
			row.ResourceType = textDocuments
			row.SingleKey = textDocument
			row.RPC = "test.v1.TestService.GetDocumentAliased"
		}),
		fixtures.Build(func(row *openapi.RouteEntry) {
			row.Method = textGetUpper
			row.PathTemplate = "/api/v1/aliases/{alias}"
			row.Role = openapi.RoleGetOne
			row.ResourceType = textDocuments
			row.SingleKey = textDocument
			row.RPC = "test.v1.TestService.GetDocumentAliased"
		}),
		fixtures.Build(func(row *openapi.RouteEntry) {
			row.Method = textGetUpper
			row.PathTemplate = textAPIV1Documents
			row.Role = openapi.RoleList
			row.ResourceType = textDocuments
			row.CollectionKey = textDocuments
			row.RPC = "test.v1.TestService.ListDocuments"
		}),
		{
			Method:          textPost,
			PathTemplate:    textAPIV1Documents,
			Role:            openapi.RoleCreate,
			ResourceType:    textDocuments,
			SingleKey:       textDocument,
			RequestEnvelope: true,
			RPC:             "test.v1.TestService.CreateDocument",
			CollectionKey:   "",
			PaginationKey:   "",
		},
		fixtures.Build(func(row *openapi.RouteEntry) {
			row.Method = textDeleteUpper
			row.PathTemplate = textAPIV1DocumentsDocument
			row.Role = openapi.RoleDelete
			row.RPC = "test.v1.TestService.DeleteDocument"
		}),
	}
}

// testRouteTableEmitsEntryPerHTTPRPCDocumentRows lists part of the
// testRouteTableEmitsEntryPerHTTPRPCRows rows.
func testRouteTableEmitsEntryPerHTTPRPCDocumentRows() []openapi.RouteEntry {
	return []openapi.RouteEntry{
		fixtures.Build(func(row *openapi.RouteEntry) {
			row.Method = textGetUpper
			row.PathTemplate = textAPIV1DocumentsDocument
			row.Role = openapi.RoleGetOne
			row.ResourceType = textDocuments
			row.SingleKey = textDocument
			row.RPC = "test.v1.TestService.GetDocument"
		}),
		{
			Method:          textPatch,
			PathTemplate:    textAPIV1DocumentsDocument,
			Role:            openapi.RoleUpdate,
			ResourceType:    textDocuments,
			SingleKey:       textDocument,
			RequestEnvelope: true,
			RPC:             "test.v1.TestService.UpdateDocument",
			CollectionKey:   "",
			PaginationKey:   "",
		},
		fixtures.Build(func(row *openapi.RouteEntry) {
			row.Method = textPost
			row.PathTemplate = textAPIV1DocumentsDocumentPath
			row.Role = openapi.RoleAction
			row.ResourceType = textDocuments
			row.SingleKey = textDocument
			row.RPC = "test.v1.TestService.ConfirmDocument"
		}),
	}
}

// TestRouteTable_ErrorsOnUnclassifiableRPC tests that an unclassifiable, non-exempt
// REST RPC hard-fails generation (never silently skipped).
//
// Why this test is important:
//   - Silent skipping is exactly the failure removes: an un-annotated
//     REST RPC must fail the build loudly so its envelope can't go missing.
//
// What it tests:
//   - a POST with no resource response and no action marker propagates Classify's
//     coded apperr.CodeInvalidInput, naming the offending RPC.
func TestRouteTable_ErrorsOnUnclassifiableRPC(t *testing.T) {
	t.Parallel()

	jsonapiSetter := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	fds := fixtures.BuildRouteFDS(
		t,
		jsonapiSetter.FileOptsWithService(),
		[]*descriptorpb.DescriptorProto{fixtures.EmptyMessage(textEmptyTitle)},
		[]*descriptorpb.MethodDescriptorProto{
			fixtures.Method(
				"MarkRead",
				textEmptyTitle,
				textEmptyTitle,
				jsonapiSetter.MethodOpts(
					textPost,
					"/api/v1/notifications/mark-read",
				),
			),
		},
	)

	_, err := openapi.BuildRouteTable(fds, nil)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput),
		"expected coded InvalidInput, got %v", err)
	assert.Contains(t, err.Error(), "test.v1.TestService.MarkRead")
}

// TestRouteTable_ExemptSkipsUnclassifiable tests that an unclassifiable RPC named
// in the exemption set is skipped (no entry) instead of failing the build.
//
// Why this test is important:
//   - The exemption set is what lets a genuine action/internal RPC coexist with
//     the hard-error gate, so wiring BuildRouteTable into the generator can't
//     strand the whole surface (the gate reuses the same list).
//
// What it tests:
//   - the same unclassifiable POST, when exempt, yields no error and no route
//     entry for that RPC.
func TestRouteTable_ExemptSkipsUnclassifiable(t *testing.T) {
	t.Parallel()

	jsonapiSetter := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	fds := fixtures.BuildRouteFDS(
		t,
		jsonapiSetter.FileOptsWithService(),
		[]*descriptorpb.DescriptorProto{fixtures.EmptyMessage(textEmptyTitle)},
		[]*descriptorpb.MethodDescriptorProto{
			fixtures.Method(
				"MarkRead",
				textEmptyTitle,
				textEmptyTitle,
				jsonapiSetter.MethodOpts(
					textPost,
					"/api/v1/notifications/mark-read",
				),
			),
		},
	)

	table, err := openapi.BuildRouteTable(fds, map[string]bool{
		"test.v1.TestService.MarkRead": true,
	})
	require.NoError(t, err)
	assert.Empty(t, table["testsvc"], "exempt RPC produces no route entry")
}

// TestOpenAPI_MarksDataRequiredByRole tests that Convert applies the JSON:API
// response envelope PER OPERATION from the route table's role, not by scanning
// response properties.
//
// Why this test is important:
//   - A resource-shaped POST action (Confirm) would be mis-wrapped as a single
//     resource by the property scan; the role-based rewrite must make it meta-only,
//     and LIST vs GET_ONE must produce array vs object data deterministically — the
//
// completeness exists to remove exactly this drift.
//
// What it tests:
//   - GET_ONE → {data: object} required; LIST → {data: array} + meta; ACTION →
//     meta-only (no data member).
func TestOpenAPI_MarksDataRequiredByRole(t *testing.T) {
	t.Parallel()

	spec := runDocConvert(t, docSwagger)

	// GET_ONE: single data object required.
	getEnv := successSchema(t, spec, textAPIV1DocumentsDocument, textGet)
	require.Contains(t, getEnv["required"], textData)

	getData := openapiAsMap(
		t,
		openapiAsMap(t, getEnv["properties"], textProps)[textData],
		textData,
	)
	assert.Equal(t, "object", getData[textType])

	// LIST: data array + meta.
	listEnv := successSchema(t, spec, textAPIV1Documents, textGet)
	listData := openapiAsMap(
		t,
		openapiAsMap(t, listEnv["properties"], textProps)[textData],
		textData,
	)
	assert.Equal(t, "array", listData[textType])
	assert.Contains(t, openapiAsMap(t, listEnv["properties"], textProps), textMeta)

	// ACTION: meta-only, never a data resource.
	confirmEnv := successSchema(
		t,
		spec,
		textAPIV1DocumentsDocumentPath,
		"post",
	)
	confirmProps := openapiAsMap(t, confirmEnv["properties"], textProps)
	assert.Contains(t, confirmProps, textMeta)
	assert.NotContains(t, confirmProps, textData,
		"a resource-shaped ACTION must be meta-only, not wrapped as a single resource")
}

// TestOpenAPI_WriteOpsGetRequestEnvelope tests that CREATE/UPDATE request bodies
// are enveloped as {data:{type,attributes}} while a read op has no request body.
//
// Why this test is important:
//   - The generated client must send the JSON:API request envelope the runtime
//     expects for writes; a bare protobuf body would be rejected. Reads must not
//     grow a spurious body.
//
// What it tests:
//   - POST (CREATE) and PATCH (UPDATE) request bodies become
//     application/vnd.api+json whose data.attributes $ref names the request's
//     <Request>Attributes schema; GET has no requestBody.
func TestOpenAPI_WriteOpsGetRequestEnvelope(t *testing.T) {
	t.Parallel()

	spec := runDocConvert(t, docSwagger)
	paths := openapiAsMap(t, spec[textPaths], textPaths)

	assertEnvelopedBody := func(path, method, attrsSchema string) {
		op := openapiAsMap(t, openapiAsMap(t, paths[path], path)[method], method)
		rb := openapiAsMap(t, op["requestBody"], "requestBody")
		content := openapiAsMap(t, rb["content"], "content")
		require.Contains(t, content, "application/vnd.api+json")

		schema := openapiAsMap(
			t,
			openapiAsMap(t, content["application/vnd.api+json"], "aj")["schema"],
			"schema",
		)
		require.Contains(t, schema["required"], textData)

		data := openapiAsMap(
			t,
			openapiAsMap(t, schema["properties"], textProps)[textData],
			textData,
		)
		dataProps := openapiAsMap(t, data["properties"], "dp")
		require.Contains(t, dataProps, textType)

		attrs := openapiAsMap(t, dataProps["attributes"], "attributes")
		assert.Equal(t, "#/components/schemas/"+attrsSchema, attrs[textRef],
			"%s %s attributes schema", method, path)
	}

	assertEnvelopedBody(textAPIV1Documents, "post", "CreateDocumentRequestAttributes")
	assertEnvelopedBody(
		textAPIV1DocumentsDocument,
		"patch",
		"UpdateDocumentRequestAttributes",
	)

	// The request attribute schemas were emitted.
	schemas := openapiAsMap(t,
		openapiAsMap(t, spec["components"], "components")["schemas"], "schemas")
	assert.Contains(t, schemas, "CreateDocumentRequestAttributes")
	assert.Contains(t, schemas, "UpdateDocumentRequestAttributes")

	// GET has no request body.
	getOp := openapiAsMap(t,
		openapiAsMap(t, paths[textAPIV1DocumentsDocument], textP)[textGet], textGet)
	assert.NotContains(t, getOp, "requestBody")
}

// TestOpenAPI_RouteLookupNormalizesBasePath tests that the (method, path) route
// lookup matches even when the OpenAPI path key omits the service base path.
//
// Why this test is important:
//   - A base-path-prefix mismatch between the generated path key and
//     RouteEntry.PathTemplate would make the lookup silently miss, leaving the
//     response unenveloped (the exact regression the spec-review flagged).
//
// What it tests:
//   - A swagger whose LIST path key is relative ("/documents", no "/api/v1" prefix)
//     still resolves to its RouteEntry and gets the collection envelope.
func TestOpenAPI_RouteLookupNormalizesBasePath(t *testing.T) {
	t.Parallel()

	relSwagger := `{
      "swagger": "2.0", "info": {"title": "testsvc"},
      "paths": {"/documents": {"get": {"operationId": "ListDocuments",
        "responses": {"200": {"description": "ok",
          "schema": {"$ref": "#/definitions/ListDocumentsResponse"}}}}}},
      "definitions": {
        "Document": {"type": "object", "properties": {"id": {"type": "string"},
          "title": {"type": "string"}}},
        "ListDocumentsResponse": {"type": "object",
          "properties": {"documents": {"type": "array",
          "items": {"$ref": "#/definitions/Document"}}}}
      }
    }`
	spec := runDocConvert(t, relSwagger)
	env := successSchema(t, spec, "/documents", textGet)
	data := openapiAsMap(
		t,
		openapiAsMap(t, env["properties"], textProps)[textData],
		textData,
	)
	assert.Equal(
		t,
		"array",
		data[textType],
		"relative path key must still resolve to its LIST RouteEntry after base-path "+
			"normalization",
	)
}

// TestOpenAPI_ResourceKeyConflictStillErrors tests that the resource-key conflict
// validation still runs through the route-table-driven Convert.
//
// Why this test is important:
//   - AC #4 requires validateResourceKeys keep guarding ambiguous property-based
//     matches; the per-operation refactor must not route around it.
//
// What it tests:
//   - Two resources sharing a single key make Convert fail, even on the new path.
func TestOpenAPI_ResourceKeyConflictStillErrors(t *testing.T) {
	t.Parallel()

	jsonapiSetter := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	messages := []*descriptorpb.DescriptorProto{
		fixtures.ResourceMessage(
			&jsonapiSetter,
			textDocumentTitle,
			textDocuments,
			textDocument,
			"",
		),
		fixtures.ResourceMessage(
			&jsonapiSetter,
			"Other",
			"others",
			textDocument,
			"",
		), // shares single key "document"
		fixtures.EmptyMessage(textEmptyTitle),
	}
	methods := []*descriptorpb.MethodDescriptorProto{
		fixtures.Method(textGetdocument, textEmptyTitle, textDocumentTitle,
			jsonapiSetter.MethodOpts(textGetUpper, textAPIV1DocumentsDocument)),
	}
	fds := fixtures.BuildRouteFDS(
		t,
		jsonapiSetter.FileOptsWithService(),
		messages,
		methods,
	)

	descPath := filepath.Join(t.TempDir(), textDBinpb)
	require.NoError(t, os.WriteFile(descPath, fds, writeFileInput))

	inDir := filepath.Join(t.TempDir(), textIn)
	require.NoError(
		t,
		os.MkdirAll(filepath.Join(inDir, textTest, textV1), mkdirAllInputErrors),
	)
	require.NoError(t, os.WriteFile(
		filepath.Join(inDir, textTest, textV1, textTestSwaggerJSON),
		[]byte(`{"swagger":"2.0","info":{"title":"t"},"paths":{}}`), writeFileInput,
	))

	_, err := openapi.Convert(
		fsys.NewOSFS(),
		inDir,
		filepath.Join(t.TempDir(), textOut),
		descPath,
		nil, nil,
	)
	require.Error(t, err, "two resources sharing a key must fail validateResourceKeys")
}

// TestRouteTable_WritesRuntimeTablePerService tests that WriteRuntimeRouteTable
// emits a valid-Go routes.go per service importing the shared runtime type.
//
// Why this test is important:
//   - The generated runtime table is the contract consumes; it must be
//     valid Go importing the runtime jsonapi.RouteEntry type (a var Routes literal),
//     or the middleware can't wire it.
//
// What it tests:
//   - Over a synthetic route table, one <service>/routes.go is written per service,
//     each declaring its package, importing the runtime package, the routes' shared
//     BasePath, and a var Routes []jsonapi.RouteEntry literal; and the emitted file
//     parses as valid Go.
func TestRouteTable_WritesRuntimeTablePerService(t *testing.T) {
	t.Parallel()

	s := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	fds := fixtures.BuildRouteFDS(t, s.FileOptsWithService(),
		docMessages(&s), docMethods(&s))
	routes, err := openapi.BuildRouteTable(fds, nil)
	require.NoError(t, err)

	const runtimeImport = "example.com/rt/jsonapi"

	outDir := t.TempDir()
	require.NoError(
		t,
		openapi.WriteRuntimeRouteTable(fsys.NewOSFS(), routes, outDir, runtimeImport),
	)

	gen, err := os.ReadFile(filepath.Clean(filepath.Join(outDir, "testsvc", "routes.go")))
	require.NoError(t, err)

	src := string(gen)

	assert.Contains(t, src, "package testsvc")
	assert.Contains(t, src, `import jsonapi "example.com/rt/jsonapi"`)
	assert.Contains(t, src, "var Routes = []jsonapi.RouteEntry{")
	assert.Contains(t, src, `const BasePath = "/api/v1/documents"`)
	assert.Contains(t, src, `Role: "LIST"`)
	assert.Contains(t, src, `Role: "CREATE"`)
	assert.Contains(t, src, `RequestEnvelope: true`)
	assert.Contains(t, src, `PathTemplate: "/api/v1/documents/{document_id}/confirm"`)
	assert.Contains(t, src, `RPC: "test.v1.TestService.ListDocuments"`)

	// The emitted file must be valid Go (format.Source in the emitter guarantees
	// it; parse here to lock that in as observable behavior).
	_, parseErr := parser.ParseFile(
		token.NewFileSet(),
		"routes.go",
		gen,
		parser.AllErrors,
	)
	require.NoError(t, parseErr, "generated routes.go must be valid Go")
}

// TestOpenAPI_ResourcelessGetOneIsMetaOnly tests that a GET_ONE whose response
// carries no (jsonapi.v1.resource) is wrapped meta-only, not with a dangling
// attributes $ref.
//
// Why this test is important:
//   - GET_ONE is classified from the verb + trailing-{id} path regardless of
//     resource keys, so a resource-less such response must degrade gracefully
//     (meta-only, matching the by-property fallback) instead of emitting an
//     attributes ref to a schema that was never created — a broken client type.
//
// What it tests:
//   - A GET on /api/v1/notes/{note_id} whose response message has no resource
//     annotation yields a {meta} envelope with no data member.
func TestOpenAPI_ResourcelessGetOneIsMetaOnly(t *testing.T) {
	t.Parallel()

	jsonapiSetter := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	messages := []*descriptorpb.DescriptorProto{
		fixtures.EmptyMessage(textEmptyTitle),
		fixtures.EmptyMessage("NoteResp"), // no (jsonapi.v1.resource)
	}
	methods := []*descriptorpb.MethodDescriptorProto{
		fixtures.Method("GetNote", textEmptyTitle, "NoteResp",
			jsonapiSetter.MethodOpts(textGetUpper, "/api/v1/notes/{note_id}")),
	}
	descPath, routes, inDir := openAPIFixtureDirs(
		t,
		fixtures.BuildRouteFDS(t, jsonapiSetter.FileOptsWithService(), messages, methods),
	)

	swagger := `{"swagger":"2.0","info":{"title":"t"},
      "paths":{"/api/v1/notes/{note_id}":{"get":{"operationId":"GetNote",
        "responses":{"200":{"description":"ok",
          "schema":{"$ref":"#/definitions/NoteResponse"}}}}}},
      "definitions":{"NoteResponse":{"type":"object",
        "properties":{"status":{"type":"string"}}}}}`

	testOpenAPIResourcelessGetOneIsMetaOnlyChecksSpec(t, descPath, routes, inDir, swagger)
}

// testOpenAPIResourcelessGetOneIsMetaOnlyChecksSpec continues
// TestOpenAPI_ResourcelessGetOneIsMetaOnly: checks spec.
func testOpenAPIResourcelessGetOneIsMetaOnlyChecksSpec(
	t *testing.T,
	descPath string,
	routes map[string][]openapi.RouteEntry,
	inDir, swagger string,
) {
	t.Helper()

	var err error

	require.NoError(t, os.WriteFile(
		filepath.Join(
			inDir,
			textTest,
			textV1,
			textTestSwaggerJSON,
		),
		[]byte(swagger),
		writeFileInput,
	))

	res, err := openapi.Convert(
		fsys.NewOSFS(),
		inDir,
		filepath.Join(t.TempDir(), textOut),
		descPath,
		routes, nil,
	)
	require.NoError(t, err)

	data, err := os.ReadFile(res.Written[0])
	require.NoError(t, err)

	var spec map[string]any

	require.NoError(t, json.Unmarshal(data, &spec))

	env := successSchema(t, spec, "/api/v1/notes/{note_id}", textGet)
	props := openapiAsMap(t, env["properties"], textProps)
	assert.Contains(t, props, textMeta)
	assert.NotContains(t, props, textData, "a resource-less GET_ONE must be meta-only")
}

const (
	// mkdirAllInputConvert is the perm passed to MkdirAll in runDocConvert.
	mkdirAllInputConvert = 0o750
)

const (
	// textDocumentlist is the literal "DocumentList".
	textDocumentlist = "DocumentList"
)

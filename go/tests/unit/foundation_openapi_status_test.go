package unit_test

// Tests for the per-role success-status renumbering + role-derived coded error
// responses in the JSON:API OpenAPI rewrite (rewriteOperationByRole). The base
// grpc-gateway swagger emits every success as 200 and only a `default` error; the
// rewrite must renumber writes (CREATE→201, DELETE→204) to what the JSON:API
// gateway actually returns and enumerate the coded errors a role can produce, or
// the generated clients report the wrong status and no coded errors. Shares the
// synthetic-FDS helpers (runDocConvert/docSwagger/fixtures.NewJSONAPISetter/…) from
// foundation_openapi_routetable_test.go.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/fsys"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/openapi"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// opResponses returns the responses map for paths[path][method] of a converted spec.
func opResponses(t *testing.T, spec map[string]any, path, method string) map[string]any {
	t.Helper()
	paths := openapiAsMap(t, spec["paths"], "paths")
	op := openapiAsMap(t, openapiAsMap(t, paths[path], path)[method], method)
	return openapiAsMap(t, op["responses"], "responses")
}

// requireCodedError asserts a response code exists and carries the JSON:API error
// envelope ($ref JsonApiErrorResponse under application/vnd.api+json).
func requireCodedError(t *testing.T, responses map[string]any, code string) {
	t.Helper()
	resp := openapiAsMap(t, responses[code], code)
	content := openapiAsMap(t, resp["content"], code+".content")
	require.Contains(t, content, "application/vnd.api+json",
		"error %s must carry the JSON:API error content type", code)
}

// TestOpenAPI_RoleSuccessAndErrorCodes tests that the JSON:API rewrite renumbers the
// success status by role and emits the role-derived coded error responses.
//
// Why this test is important:
//   - The generated OpenAPI is the client contract. Before this, every create/delete
//     reported HTTP 200 (the runtime returns 201/204) and only a generic `default`
//     error — so generated clients had the wrong status codes and no coded errors.
//
// What it tests:
//   - CREATE → 201 (no 200), LIST/GET_ONE/UPDATE/ACTION → 200; the role-derived
//     error set per role (400/401/403 always; +404 on {id} paths; +409 on
//     create/update); and the removal of the generic `default` error.
func TestOpenAPI_RoleSuccessAndErrorCodes(t *testing.T) {
	t.Parallel()
	spec := runDocConvert(t, docSwagger)

	// CREATE (POST /documents, top-level collection): 201, no 200/404, +409.
	create := opResponses(t, spec, "/api/v1/documents", "post")
	require.Contains(t, create, "201", "CREATE must return 201")
	require.NotContains(t, create, "200", "CREATE must not keep the inherited 200")
	require.NotContains(t, create, "default", "the generic default error is replaced")
	for _, c := range []string{"400", "401", "403", "409"} {
		requireCodedError(t, create, c)
	}
	require.NotContains(t, create, "404", "a top-level create has no parent to 404")

	// LIST (GET /documents, top-level): 200, +400/401/403 only.
	list := opResponses(t, spec, "/api/v1/documents", "get")
	require.Contains(t, list, "200")
	for _, c := range []string{"400", "401", "403"} {
		requireCodedError(t, list, c)
	}
	require.NotContains(t, list, "404")
	require.NotContains(t, list, "409")

	// GET_ONE (GET /documents/{id}): 200, +400/401/403/404, no 409.
	getOne := opResponses(t, spec, "/api/v1/documents/{document_id}", "get")
	require.Contains(t, getOne, "200")
	for _, c := range []string{"400", "401", "403", "404"} {
		requireCodedError(t, getOne, c)
	}
	require.NotContains(t, getOne, "409")

	// UPDATE (PATCH /documents/{id}): 200, +400/401/403/404/409.
	update := opResponses(t, spec, "/api/v1/documents/{document_id}", "patch")
	require.Contains(t, update, "200")
	for _, c := range []string{"400", "401", "403", "404", "409"} {
		requireCodedError(t, update, c)
	}

	// ACTION (POST /documents/{id}/confirm): 200, +400/401/403/404, no 409.
	action := opResponses(t, spec, "/api/v1/documents/{document_id}/confirm", "post")
	require.Contains(t, action, "200")
	for _, c := range []string{"400", "401", "403", "404"} {
		requireCodedError(t, action, c)
	}
	require.NotContains(t, action, "409")
}

// TestOpenAPI_DeleteEmits204NoBody tests that a DELETE role emits a bodyless 204.
//
// Why this test is important:
//   - The runtime gateway returns 204 (no content) for a delete; the prior spec
//     reported 200 with an empty JSON body, so clients typed the wrong status.
//
// What it tests:
//   - A DELETE operation's success response is 204 (not 200) with no content body,
//     and carries the {id}-path error set (400/401/403/404, no 409).
func TestOpenAPI_DeleteEmits204NoBody(t *testing.T) {
	t.Parallel()
	s := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	messages := []*descriptorpb.DescriptorProto{fixtures.EmptyMessage("Empty")}
	methods := []*descriptorpb.MethodDescriptorProto{
		fixtures.Method("DeleteThing", "Empty", "Empty",
			s.MethodOpts("DELETE", "/api/v1/things/{thing_id}", false)),
	}
	fds := fixtures.BuildRouteFDS(
		t,
		s.FileOptsWithService(),
		messages,
		methods,
	)

	descPath := filepath.Join(t.TempDir(), "d.binpb")
	require.NoError(t, os.WriteFile(descPath, fds, 0o644))
	routes, err := openapi.BuildRouteTable(fds, nil)
	require.NoError(t, err)

	inDir := filepath.Join(t.TempDir(), "in")
	require.NoError(t, os.MkdirAll(filepath.Join(inDir, "test", "v1"), 0o755))
	swagger := `{"swagger":"2.0","info":{"title":"t"},
      "paths":{"/api/v1/things/{thing_id}":{"delete":{"operationId":"DeleteThing",
        "parameters":[{"name":"thing_id","in":"path","required":true,"type":"string"}],
        "responses":{"200":{"description":"ok","schema":{"type":"object"}}}}}},
      "definitions":{}}`
	require.NoError(t, os.WriteFile(
		filepath.Join(inDir, "test", "v1", "test.swagger.json"), []byte(swagger), 0o644,
	))

	res, err := openapi.Convert(
		fsys.NewOSFS(),
		inDir,
		filepath.Join(t.TempDir(), "out"),
		descPath,
		routes, nil,
	)
	require.NoError(t, err)
	data, err := os.ReadFile(res.Written[0])
	require.NoError(t, err)
	var spec map[string]any
	require.NoError(t, json.Unmarshal(data, &spec))

	resp := opResponses(t, spec, "/api/v1/things/{thing_id}", "delete")
	require.Contains(t, resp, "204", "DELETE must return 204")
	require.NotContains(t, resp, "200", "DELETE must not keep the inherited 200")
	del204 := openapiAsMap(t, resp["204"], "204")
	require.NotContains(t, del204, "content", "a 204 has no response body")
	for _, c := range []string{"400", "401", "403", "404"} {
		requireCodedError(t, resp, c)
	}
	require.NotContains(t, resp, "409")
	require.NotContains(t, resp, "default")
}

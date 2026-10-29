package unit_test

// Regression test for the meta-only envelope of an ACTION whose response message is EMPTY
// (no fields) — e.g. PauseSync/ResumeSync/AbortMultipartUpload. The ACTION role reaches
// wrapMetaResponse directly (bypassing wrapResponseSchema's empty-props early return), so
// an empty response must NOT emit a dangling `<name>Meta` $ref that no schema defines —
// that broke a generated client (openapi-typescript "Can't resolve $ref"). Shares the
// synthetic-FDS helpers from tests/fixtures and foundation_openapi_routetable_test.go.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/fsys"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/openapi"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// TestOpenAPI_EmptyActionMetaHasNoDanglingRef tests that an ACTION whose response
// message carries no fields produces a resolvable meta envelope.
//
// Why this test is important:
//   - a generated client (openapi-typescript) hard-fails on a `$ref` to a schema that
//     was never emitted; an empty-response action (the common case for the backfilled
//     sync/upload actions) must still yield a valid spec.
//
// What it tests:
//   - For a POST ACTION whose response has no properties, the 200 envelope's `meta`
//     property does not reference a missing component (it is inline, or a resolvable
//     $ref) — no dangling reference remains in the generated spec.
func TestOpenAPI_EmptyActionMetaHasNoDanglingRef(t *testing.T) {
	t.Parallel()
	s := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	messages := []*descriptorpb.DescriptorProto{
		fixtures.EmptyMessage("Empty"),
		fixtures.EmptyMessage(
			"PauseResp",
		), // empty action response — no (jsonapi.v1.resource), no fields
	}
	methods := []*descriptorpb.MethodDescriptorProto{
		fixtures.Method("PauseThing", "Empty", "PauseResp",
			s.MethodOpts("POST", "/api/v1/things/{thing_id}/pause", true)), // action:true
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
	// The swagger response schema is a truly empty object (no properties) — exactly
	// what protoc-gen-openapiv2 emits for an empty response message.
	swagger := `{"swagger":"2.0","info":{"title":"t"},
      "paths":{"/api/v1/things/{thing_id}/pause":{"post":{"operationId":"PauseThing",
        "responses":{"200":{"description":"ok",
          "schema":{"$ref":"#/definitions/PauseThingResponse"}}}}}},
      "definitions":{"PauseThingResponse":{"type":"object"}}}`
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

	env := successSchema(t, spec, "/api/v1/things/{thing_id}/pause", "post")
	props := openapiAsMap(t, env["properties"], "props")
	require.Contains(t, props, "meta", "an ACTION envelope carries a meta member")

	meta := openapiAsMap(t, props["meta"], "meta")
	if ref, ok := meta["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		schemas := openapiAsMap(t,
			openapiAsMap(t, spec["components"], "components")["schemas"], "schemas")
		assert.Contains(t, schemas, name,
			"meta $ref must resolve to an emitted schema (no dangling ref)")
	}
}

package unit_test

// Black-box tests for google.api.http custom-verb paths ("/things/{thing_id}:launch")
// in the two JSON:API route lookups: the runtime matcher the middleware uses
// (foundation/jsonapi Config.MatchRoute) and the OpenAPI converter's operation lookup
// (foundation/openapi Convert). Both must treat "{param}:verb" as a parameter plus a
// literal verb, or the action's response is left unenveloped at runtime while the
// generated spec promises the by-property resource envelope.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/fsys"
	codec "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/jsonapi"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/openapi"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

const (
	// textThingLaunchTemplate is the custom-verb action's path template.
	textThingLaunchTemplate = "/api/v1/things/{thing_id}:launch"
	// textThingTemplate is the plain instance path template the verb must beat.
	textThingTemplate = "/api/v1/things/{thing_id}"
	// textThing is the resource's single key.
	textThing = "thing"
	// textThings is the resource's JSON:API type.
	textThings = "things"
)

// TestJSONAPIMatchRoute_CustomVerb tests that the runtime matcher resolves a
// "{param}:verb" template against a concrete "<id>:verb" segment.
//
// Why this test is important:
//   - The middleware picks the envelope from the matched route's role. A custom-verb
//     action that never matches passes its handler's raw body through, which breaks
//     every client typed from the generated spec.
//
// What it tests:
//   - POST /things/t1:launch resolves to the ACTION row, not the {thing_id} row; an
//     empty id (":launch") or another verb (":close") does not match the ACTION row.
func TestJSONAPIMatchRoute_CustomVerb(t *testing.T) {
	t.Parallel()

	cfg := codec.Config{
		BasePath: "/api/v1",
		Routes: []codec.RouteEntry{
			fixtures.Build(func(row *codec.RouteEntry) {
				row.Method = textPost
				row.PathTemplate = textThingTemplate
				row.Role = codec.RoleUpdate
			}),
			fixtures.Build(func(row *codec.RouteEntry) {
				row.Method = textPost
				row.PathTemplate = textThingLaunchTemplate
				row.Role = codec.RoleAction
				row.ResourceType = textThings
				row.SingleKey = textThing
			}),
		},
	}

	launch, found := cfg.MatchRoute(textPost, "/api/v1/things/t1:launch")
	require.True(t, found)
	assert.Equal(t, codec.RoleAction, launch.Role, "the verb segment matches as literal")

	for _, path := range []string{"/api/v1/things/:launch", "/api/v1/things/t1:close"} {
		row, ok := cfg.MatchRoute(textPost, path)
		if ok {
			assert.NotEqual(t, codec.RoleAction, row.Role, path)
		}
	}
}

// TestOpenAPI_CustomVerbActionIsMetaOnly tests that the converter finds the route
// row of a custom-verb ACTION and gives it the meta-only envelope.
//
// Why this test is important:
//   - On a lookup miss the converter falls back to wrapping by response property,
//     so an action returning a resource key was typed as a {data} resource document
//     while the gateway returns {meta}: the client read a member that never arrives.
//
// What it tests:
//   - An action:true POST on /things/{thing_id}:launch whose response holds the
//     resource's single key yields a 200 envelope with meta and no data member,
//     though the swagger path names the parameter in camelCase.
func TestOpenAPI_CustomVerbActionIsMetaOnly(t *testing.T) {
	t.Parallel()

	jsonapiSetter := fixtures.NewJSONAPISetter(t, fixtures.JSONAPIAnnotations(t))
	messages := []*descriptorpb.DescriptorProto{
		fixtures.ResourceMessage(&jsonapiSetter, "Thing", textThings, textThing, ""),
		fixtures.EmptyMessage(textEmptyTitle),
	}
	methods := []*descriptorpb.MethodDescriptorProto{
		fixtures.Method("LaunchThing", textEmptyTitle, "Thing",
			jsonapiSetter.ActionMethodOpts(textPost, textThingLaunchTemplate)),
	}
	descPath, routes, inDir := openAPIFixtureDirs(
		t,
		fixtures.BuildRouteFDS(t, jsonapiSetter.FileOptsWithService(), messages, methods),
	)

	swagger := `{"swagger":"2.0","info":{"title":"t"},
      "paths":{"/api/v1/things/{thingId}:launch":{"post":{"operationId":"LaunchThing",
        "responses":{"200":{"description":"ok",
          "schema":{"$ref":"#/definitions/LaunchThingResponse"}}}}}},
      "definitions":{
        "Thing":{"type":"object","properties":{"id":{"type":"string"}}},
        "LaunchThingResponse":{"type":"object",
          "properties":{"thing":{"$ref":"#/definitions/Thing"}}}}}`
	require.NoError(t, os.WriteFile(
		filepath.Join(inDir, textTest, textV1, textTestSwaggerJSON),
		[]byte(swagger),
		writeFileInput,
	))

	res, err := openapi.Convert(fsys.NewOSFS(), inDir,
		filepath.Join(t.TempDir(), textOut), descPath, routes, nil)
	require.NoError(t, err)

	data, err := os.ReadFile(res.Written[0])
	require.NoError(t, err)

	var spec map[string]any

	require.NoError(t, json.Unmarshal(data, &spec))

	env := successSchema(t, spec, "/api/v1/things/{thingId}:launch", "post")
	props := openapiAsMap(t, env["properties"], textProps)
	assert.Contains(t, props, textMeta, "an ACTION envelope carries a meta member")
	assert.NotContains(t, props, textData, "an ACTION envelope carries no data member")
}

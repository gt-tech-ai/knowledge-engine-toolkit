package unit_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/jsonapi"
)

// ---------------------------------------------------------------------------
// TransformSingleResource
// ---------------------------------------------------------------------------

// TestTransformSingleResource tests how a single-resource body is converted to a JSON:API
// resource document and how it degrades.
//
// Why this test is important:
//   - This transform defines the resource-object contract (id lifted out,
//     everything else under attributes, a self link). The fallbacks matter too:
//     action responses without the resource key must become valid meta documents
//     rather than malformed ones, so the output is always spec-conformant.
//
// What it tests:
//   - Success: data.type/id are set, other fields move to attributes (id absent
//     there), and links.self is populated.
//   - A resource carrying only an id still gets an (empty) attributes object, which
//     the generated OpenAPI marks required.
//   - Missing resource key wraps the remaining fields as meta.
//   - A non-map value under the resource key falls back to the original body.
func TestTransformSingleResource(t *testing.T) {
	t.Parallel()

	cfg := jsonapi.ResourceConfig{
		Type:      "users",
		SingleKey: "user",
	}

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		body := map[string]any{
			"user": map[string]any{
				"id":          "123",
				"email":       "test@example.com",
				"displayName": "Test User",
				"active":      true,
			},
		}

		result := jsonapi.TransformSingleResource(body, cfg, "/api/v1/demo/users/123")

		data, ok := result["data"].(map[string]any)
		require.True(t, ok, "expected data key")
		assert.Equal(t, "users", data["type"])
		assert.Equal(t, "123", data["id"])

		attrs, ok := data["attributes"].(map[string]any)
		require.True(t, ok, "expected attributes")
		assert.Equal(t, "test@example.com", attrs["email"])
		assert.Equal(t, "Test User", attrs["displayName"])
		assert.Equal(t, true, attrs["active"])
		assert.Nil(t, attrs["id"], "id must not appear in attributes")

		links, ok := result["links"].(map[string]any)
		require.True(t, ok, "expected links")
		assert.Equal(t, "/api/v1/demo/users/123", links["self"])
	})

	t.Run("id-only resource still carries attributes", func(t *testing.T) {
		t.Parallel()

		body := map[string]any{"user": map[string]any{"id": "123"}}

		result := jsonapi.TransformSingleResource(body, cfg, "/api/v1/demo/users/123")

		data, ok := result["data"].(map[string]any)
		require.True(t, ok, "expected data key")
		attrs, ok := data["attributes"].(map[string]any)
		require.True(t, ok, "attributes must be present even when the resource has none")
		assert.Empty(t, attrs)
	})

	t.Run("missing key wraps as meta", func(t *testing.T) {
		t.Parallel()
		body := map[string]any{"other": "value"}
		result := jsonapi.TransformSingleResource(body, cfg, "/test")
		meta, ok := result["meta"].(map[string]any)
		require.True(t, ok, "expected meta key")
		assert.Equal(t, "value", meta["other"])
	})

	t.Run("non-map value falls back", func(t *testing.T) {
		t.Parallel()
		body := map[string]any{"user": "not-a-map"}
		result := jsonapi.TransformSingleResource(body, cfg, "/test")
		assert.Equal(t, body, result)
	})
}

// ---------------------------------------------------------------------------
// TransformCollection
// ---------------------------------------------------------------------------

// TestTransformCollection tests how a list body becomes a JSON:API collection, including
// pagination and empty cases.
//
// Why this test is important:
//   - Collections must always present data as an array and surface paging so
//     clients can iterate; a subtle bug (omitting the next link, or null instead
//     of [] when protobuf drops an empty repeated field) would break iteration.
//
// What it tests:
//   - Success: data is an array of resource objects, meta.totalCount is mapped,
//     and links.self/next reflect the path and page token.
//   - The bounded-count "N+" estimate flag (totalIsEstimate) is forwarded into meta
//     when present, and omitted otherwise — so a REST client can render "10,000+".
//   - An explicitly empty collection yields an empty data array with a self link.
//   - A missing collection key (protobuf omits empty repeateds) is treated as an
//     empty array, not a fallback.
func TestTransformCollection(t *testing.T) {
	t.Parallel()

	cfg := jsonapi.ResourceConfig{
		Type:          "users",
		CollectionKey: "users",
		PaginationKey: "pagination",
	}

	t.Run("success with pagination", func(t *testing.T) {
		t.Parallel()

		body := map[string]any{
			"users": []any{
				map[string]any{"id": "1", "email": "a@b.com"},
				map[string]any{"id": "2", "email": "c@d.com"},
			},
			"pagination": map[string]any{
				"nextPageToken": "tok123",
				"totalCount":    float64(45),
			},
		}

		result := jsonapi.TransformCollection(body, cfg, "/api/v1/demo/users")

		data, ok := result["data"].([]any)
		require.True(t, ok)
		assert.Len(t, data, 2)

		first := data[0].(map[string]any)
		assert.Equal(t, "users", first["type"])
		assert.Equal(t, "1", first["id"])

		meta, ok := result["meta"].(map[string]any)
		require.True(t, ok)
		assert.InDelta(t, float64(45), meta["totalCount"], 0)

		links, ok := result["links"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "/api/v1/demo/users", links["self"])
		assert.Equal(t, "/api/v1/demo/users?page_token=tok123", links["next"])
		// The exact-count case carries no estimate flag (protojson omits the default false).
		_, hasEstimate := meta["totalIsEstimate"]
		assert.False(t, hasEstimate, "an exact total must not advertise an estimate")
	})

	t.Run("over-cap total forwards the estimate flag", func(t *testing.T) {
		t.Parallel()

		body := map[string]any{
			"users": []any{map[string]any{"id": "1", "email": "a@b.com"}},
			"pagination": map[string]any{
				"totalCount":      float64(10000),
				"totalIsEstimate": true,
			},
		}

		result := jsonapi.TransformCollection(body, cfg, "/api/v1/demo/users")

		meta, ok := result["meta"].(map[string]any)
		require.True(t, ok)
		assert.InDelta(t, float64(10000), meta["totalCount"], 0)
		assert.Equal(
			t,
			true,
			meta["totalIsEstimate"],
			"the N+ estimate flag must reach the client",
		)
	})

	t.Run("empty collection", func(t *testing.T) {
		t.Parallel()

		body := map[string]any{
			"users": []any{},
		}

		result := jsonapi.TransformCollection(body, cfg, "/api/v1/demo/users")

		data, ok := result["data"].([]any)
		require.True(t, ok)
		assert.Empty(t, data)

		links, ok := result["links"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "/api/v1/demo/users", links["self"])
	})

	t.Run("missing collection key treated as empty", func(t *testing.T) {
		t.Parallel()

		// Protobuf omits empty repeated fields, so we get just pagination.
		body := map[string]any{
			"pagination": map[string]any{},
		}

		result := jsonapi.TransformCollection(body, cfg, "/api/v1/demo/users")

		data, ok := result["data"].([]any)
		require.True(t, ok)
		assert.Empty(t, data)

		links, ok := result["links"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "/api/v1/demo/users", links["self"])
	})
}

// ---------------------------------------------------------------------------
// TransformError
// ---------------------------------------------------------------------------

// TestTransformError tests that Connect/Vanguard error bodies become JSON:API error
// objects across status codes and bad input.
//
// Why this test is important:
//   - This is the uniform failure contract clients parse; the machine code and
//     status string must be correct and always present, and the transform must
//     stay robust when the upstream body is unparseable or empty so errors never
//     become a second, malformed error.
//
// What it tests:
//   - A parseable Connect error maps code/status/detail.
//   - The status string mirrors the HTTP status for a non-404 code.
//   - An unparseable body still produces a 500 error with code "unknown".
//   - An empty body yields code/status with no "detail" member at all, rather
//     than a placeholder message.
//   - No "title" member is emitted on any path.
func TestTransformError(t *testing.T) {
	t.Parallel()

	t.Run("404 with connect error", func(t *testing.T) {
		t.Parallel()

		input := []byte(`{"code":"not_found","message":"user not found"}`)
		result := jsonapi.TransformError(404, input)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(result, &parsed))

		errors, ok := parsed["errors"].([]any)
		require.True(t, ok)
		require.Len(t, errors, 1)

		errObj := errors[0].(map[string]any)
		assert.Equal(t, "not_found", errObj["code"])
		assert.Equal(t, "404", errObj["status"])
		assert.Equal(t, "user not found", errObj["detail"])
		assert.NotContains(t, errObj, "title")
	})

	t.Run("400 bad request", func(t *testing.T) {
		t.Parallel()

		input := []byte(`{"code":"invalid_argument","message":"email is required"}`)
		result := jsonapi.TransformError(400, input)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(result, &parsed))

		errObj := parsed["errors"].([]any)[0].(map[string]any)
		assert.Equal(t, "invalid_argument", errObj["code"])
		assert.Equal(t, "400", errObj["status"])
		assert.Equal(t, "email is required", errObj["detail"])
	})

	// The live path: Vanguard renders a transcoded Connect/gRPC error as a
	// google.rpc.Status whose "code" is a NUMBER, so the numeric google.rpc.Code
	// must map to its canonical snake_case name — not degrade to "unknown" (the
	// bug this guards: every REST error otherwise lost its machine-readable code).
	t.Run("404 with numeric vanguard code maps to name", func(t *testing.T) {
		t.Parallel()

		input := []byte(`{"code":5,"message":"resource not found"}`)
		result := jsonapi.TransformError(404, input)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(result, &parsed))

		errObj := parsed["errors"].([]any)[0].(map[string]any)
		assert.Equal(
			t,
			"not_found",
			errObj["code"],
			"numeric code 5 (NotFound) maps to the connect name",
		)
		assert.Equal(t, "404", errObj["status"])
		assert.Equal(t, "resource not found", errObj["detail"])
	})

	// An out-of-range numeric code has no canonical name, so it keeps the "unknown"
	// default rather than leaking a bare number as the machine-readable code.
	t.Run("out-of-range numeric code stays unknown", func(t *testing.T) {
		t.Parallel()

		input := []byte(`{"code":99,"message":"weird"}`)
		result := jsonapi.TransformError(500, input)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(result, &parsed))

		errObj := parsed["errors"].([]any)[0].(map[string]any)
		assert.Equal(t, "unknown", errObj["code"])
		assert.Equal(t, "500", errObj["status"])
	})

	t.Run("500 with unparseable body", func(t *testing.T) {
		t.Parallel()

		input := []byte(`not json`)
		result := jsonapi.TransformError(500, input)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(result, &parsed))

		errObj := parsed["errors"].([]any)[0].(map[string]any)
		assert.Equal(t, "unknown", errObj["code"])
		assert.Equal(t, "500", errObj["status"])
		assert.NotContains(t, errObj, "detail")
	})

	t.Run("empty body", func(t *testing.T) {
		t.Parallel()

		result := jsonapi.TransformError(503, nil)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal(result, &parsed))

		errObj := parsed["errors"].([]any)[0].(map[string]any)
		assert.Equal(t, "unknown", errObj["code"])
		assert.Equal(t, "503", errObj["status"])
		assert.NotContains(t, errObj, "detail")
		assert.NotContains(t, errObj, "title")
	})
}

// ---------------------------------------------------------------------------
// IsRESTRequest
// ---------------------------------------------------------------------------

// TestIsRESTRequest tests the protocol discriminator that decides whether a request gets
// JSON:API treatment.
//
// Why this test is important:
//   - This predicate is the single switch separating REST traffic (which is
//     transformed) from Connect/gRPC/gRPC-web (which must pass through); a wrong
//     answer here either corrupts binary RPC streams or fails to envelope real
//     REST responses.
//
// What it tests:
//   - A header-less request is REST, while requests with Connect-Protocol-Version
//     or an application/grpc or application/grpc-web content type are not.
func TestIsRESTRequest(t *testing.T) {
	t.Parallel()

	t.Run("plain request is REST", func(t *testing.T) {
		t.Parallel()
		r := &http.Request{Header: http.Header{}}
		assert.True(t, jsonapi.IsRESTRequest(r))
	})

	t.Run("connect protocol is not REST", func(t *testing.T) {
		t.Parallel()
		r := &http.Request{Header: http.Header{
			"Connect-Protocol-Version": []string{"1"},
		}}
		assert.False(t, jsonapi.IsRESTRequest(r))
	})

	t.Run("grpc is not REST", func(t *testing.T) {
		t.Parallel()
		r := &http.Request{Header: http.Header{
			"Content-Type": []string{"application/grpc"},
		}}
		assert.False(t, jsonapi.IsRESTRequest(r))
	})

	t.Run("grpc-web is not REST", func(t *testing.T) {
		t.Parallel()
		r := &http.Request{Header: http.Header{
			"Content-Type": []string{"application/grpc-web"},
		}}
		assert.False(t, jsonapi.IsRESTRequest(r))
	})
}

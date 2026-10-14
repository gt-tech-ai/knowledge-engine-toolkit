package unit_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	codec "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/jsonapi"
)

// TestTransformError_BodyFallbacks tests the error transformer's fallback
// branches for missing/malformed bodies and non-curated status codes.
//
// Why this test is important:
//   - Error responses are the contract clients rely on to react to failures. The
//     generated client types "code" and "status" as required, so every path —
//     including a teapot status or a body that never parsed — must still emit
//     both, or the client's non-optional field would be undefined at runtime.
//
// What it tests:
//   - Any status stringifies into "status" without consulting a lookup table.
//   - An empty or unparseable body yields code "unknown" and OMITS "detail"
//     entirely (rather than substituting a placeholder message).
//   - A parseable body overrides code and supplies detail.
func TestTransformError_BodyFallbacks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		wantCode   string
		wantStatus string
		wantDetail string // "" means the member must be absent
		body       []byte
		status     int
	}{
		{
			name:       "uncurated status still stringifies",
			status:     418,
			wantCode:   "unknown",
			wantStatus: "418",
		},
		{
			name:       "status with no http.StatusText still stringifies",
			status:     599,
			wantCode:   "unknown",
			wantStatus: "599",
		},
		{
			name:       "unparseable body keeps defaults",
			status:     400,
			body:       []byte("{not json"),
			wantCode:   "unknown",
			wantStatus: "400",
		},
		{
			name:       "body with empty message omits detail",
			status:     500,
			body:       []byte(`{"code":"internal","message":""}`),
			wantCode:   "internal",
			wantStatus: "500",
		},
		{
			name:       "parsed body overrides code+detail",
			status:     404,
			body:       []byte(`{"code":"not_found","message":"gone"}`),
			wantCode:   "not_found",
			wantStatus: "404",
			wantDetail: "gone",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var doc map[string]any
			require.NoError(
				t,
				json.Unmarshal(codec.TransformError(tt.status, tt.body), &doc),
			)
			errs, ok := doc["errors"].([]any)
			require.True(t, ok)
			require.Len(t, errs, 1)
			e := errs[0].(map[string]any)
			assert.Equal(t, tt.wantCode, e["code"])
			assert.Equal(t, tt.wantStatus, e["status"])
			if tt.wantDetail == "" {
				assert.NotContains(t, e, "detail")
			} else {
				assert.Equal(t, tt.wantDetail, e["detail"])
			}
			assert.NotContains(t, e, "title")
		})
	}
}

// TestTransformResource_Fallbacks tests the single/collection transformers'
// non-resource and malformed-shape branches.
//
// Why this test is important:
//   - Action endpoints return status (not a resource) and protobuf omits empty
//     collections; if the transformer mis-handled these it would emit an invalid
//     JSON:API document (no data/meta/errors) or drop the payload.
//
// What it tests:
//   - A body without the single key becomes a meta-only document; a non-map single
//     value passes through; a missing collection key yields an empty data array; a
//     non-array collection value passes through.
func TestTransformResource_Fallbacks(t *testing.T) {
	t.Parallel()
	cfg := codec.ResourceConfig{
		Type:          "users",
		SingleKey:     "user",
		CollectionKey: "users",
	}

	// No single key → meta-only document (action endpoint response).
	meta := codec.TransformSingleResource(map[string]any{"sent": 5.0}, cfg, "")
	assert.Equal(t, map[string]any{"meta": map[string]any{"sent": 5.0}}, meta)

	// Single value not a map → passed through unchanged.
	notMap := map[string]any{"user": "scalar"}
	assert.Equal(t, notMap, codec.TransformSingleResource(notMap, cfg, ""))

	// Missing collection key → empty data array (not a fallback to the body).
	empty := codec.TransformCollection(map[string]any{}, cfg, "")
	assert.Equal(t, []any{}, empty["data"])

	// Collection value not an array → passed through unchanged.
	badColl := map[string]any{"users": "scalar"}
	assert.Equal(t, badColl, codec.TransformCollection(badColl, cfg, ""))

	// Resource present WITH a self link and extra sibling fields → data object,
	// links.self, and the extras collected into meta (the GetCurrentUser shape).
	full := codec.TransformSingleResource(
		map[string]any{
			"user":         map[string]any{"id": "1", "email": "a@b.com"},
			"currentOrgId": "o1",
		}, cfg, "/api/users/1",
	)
	assert.Equal(t, "/api/users/1", full["links"].(map[string]any)["self"])
	assert.Equal(t, map[string]any{"currentOrgId": "o1"}, full["meta"])
	data := full["data"].(map[string]any)
	assert.Equal(t, "users", data["type"])
	assert.Equal(t, "1", data["id"])
}

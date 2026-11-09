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

	tests := testTransformErrorBodyFallbacksTable()
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var doc map[string]any

			require.NoError(
				t,
				json.Unmarshal(
					codec.TransformError(testCase.status, testCase.body),
					&doc,
				),
			)

			errs, ok := doc[textErrors].([]any)
			require.True(t, ok)
			require.Len(t, errs, 1)

			lookup2 := asType[map[string]any](t, errs[0])
			assert.Equal(t, testCase.wantCode, lookup2["code"])
			assert.Equal(t, testCase.wantStatus, lookup2[textStatus])

			if testCase.wantDetail == "" {
				assert.NotContains(t, lookup2, "detail")
			} else {
				assert.Equal(t, testCase.wantDetail, lookup2["detail"])
			}

			assert.NotContains(t, lookup2, textTitle)
		})
	}
}

// testTransformErrorBodyFallbacksRow is one row of testTransformErrorBodyFallbacksTable.
type testTransformErrorBodyFallbacksRow struct {
	name       string
	wantCode   string
	wantStatus string
	wantDetail string // "" means the member must be absent
	body       []byte
	status     int
}

// testTransformErrorBodyFallbacksTable builds the table TestTransformError_BodyFallbacks
// runs.
func testTransformErrorBodyFallbacksTable() []testTransformErrorBodyFallbacksRow {
	return []testTransformErrorBodyFallbacksRow{
		{
			name:       "uncurated status still stringifies",
			status:     status418,
			wantCode:   textUnknown,
			wantStatus: "418",
		},
		{
			name:       "status with no http.StatusText still stringifies",
			status:     status599,
			wantCode:   textUnknown,
			wantStatus: "599",
		},
		{
			name:       "unparseable body keeps defaults",
			status:     status400,
			body:       []byte(textNotStructured),
			wantCode:   textUnknown,
			wantStatus: text400,
		},
		{
			name:       "body with empty message omits detail",
			status:     status500,
			body:       []byte(`{"code":"internal","message":""}`),
			wantCode:   textInternal,
			wantStatus: "500",
		},
		{
			name:       "parsed body overrides code+detail",
			status:     status404,
			body:       []byte(`{"code":"not_found","message":"gone"}`),
			wantCode:   "not_found",
			wantStatus: text404,
			wantDetail: textGone,
		},
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
		Type:          textUsers,
		SingleKey:     textUser,
		CollectionKey: textUsers,
	}

	// No single key → meta-only document (action endpoint response).
	meta := codec.TransformSingleResource(map[string]any{"sent": 5.0}, cfg, "")
	assert.Equal(t, map[string]any{textMeta: map[string]any{"sent": 5.0}}, meta)

	// Single value not a map → passed through unchanged.
	notMap := map[string]any{textUser: textScalar}
	assert.Equal(t, notMap, codec.TransformSingleResource(notMap, cfg, ""))

	// Missing collection key → empty data array (not a fallback to the body).
	empty := codec.TransformCollection(map[string]any{}, cfg, "")
	assert.Equal(t, []any{}, empty[textData])

	// Collection value not an array → passed through unchanged.
	badColl := map[string]any{textUsers: textScalar}
	assert.Equal(t, badColl, codec.TransformCollection(badColl, cfg, ""))

	// Resource present WITH a self link and extra sibling fields → data object,
	// links.self, and the extras collected into meta (the GetCurrentUser shape).
	full := codec.TransformSingleResource(
		map[string]any{
			textUser:       map[string]any{textID: "1", textEmail: textABCom},
			"currentOrgId": "o1",
		}, cfg, "/api/users/1",
	)
	assert.Equal(t, "/api/users/1", asType[map[string]any](t, full["links"])["self"])
	assert.Equal(t, map[string]any{"currentOrgId": "o1"}, full[textMeta])

	data := asType[map[string]any](t, full[textData])
	assert.Equal(t, textUsers, data[textType])
	assert.Equal(t, "1", data[textID])
}

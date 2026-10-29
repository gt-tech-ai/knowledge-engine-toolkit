package unit_test

// Black-box test for the openapi codegen package's JSON:API meta-envelope
// wrapping. It drives the public openapi.Convert (the only exported entry) with a
// real buf descriptor plus a synthetic action-endpoint swagger file, then asserts
// on the generated OpenAPI spec — so the unexported wrapMetaResponse is exercised
// without touching any unexported symbol.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openapiAsMap type-asserts a nested JSON value to an object, failing the test
// with a path-labelled message when the shape is unexpected.
func openapiAsMap(t *testing.T, v any, label string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "%s should be a JSON object, got %T", label, v)
	return m
}

// TestOpenAPI_ConvertKeepsRefSiblings tests that a $ref'd field keeps the keys
// protoc-gen-openapiv2 writes beside the $ref through the OpenAPI 3 conversion.
//
// Why this test is important:
//   - The converter drops a $ref's siblings (OpenAPI 3.0 ignores them), but they carry a
//     field's description and readOnly (proto OUTPUT_ONLY), which Redoc and
//     openapi-typescript render as the field's docs and as `readonly`; losing them
//     silently strips the API reference and lets the client write output-only fields.
//
// What it tests:
//   - A property and an array's items that $ref another definition keep their
//     description and readOnly, with the $ref rewritten to #/components/schemas.
//   - The spec declares the requested API-key header scheme and requires it.
func TestOpenAPI_ConvertKeepsRefSiblings(t *testing.T) {
	t.Parallel()
	swagger := strings.Replace(docSwagger, `"definitions": {`, `"definitions": {
    "DocumentNote": {"type": "object", "properties": {
      "doc": {"$ref": "#/definitions/Document", "description": "the noted document.",
        "readOnly": true},
      "related": {"type": "array", "items": {"$ref": "#/definitions/Document",
        "description": "a related document."}}}},`, 1)

	spec := runDocConvert(t, swagger)

	components := openapiAsMap(t, spec["components"], "components")
	note := openapiAsMap(
		t,
		openapiAsMap(t, components["schemas"], "schemas")["DocumentNote"],
		"DocumentNote",
	)
	props := openapiAsMap(t, note["properties"], "properties")
	assert.Equal(t, map[string]any{
		"$ref":        "#/components/schemas/Document",
		"description": "the noted document.",
		"readOnly":    true,
	}, props["doc"])
	related := openapiAsMap(t, props["related"], "related")
	assert.Equal(t, map[string]any{
		"$ref":        "#/components/schemas/Document",
		"description": "a related document.",
	}, related["items"])

	schemes := openapiAsMap(t, components["securitySchemes"], "securitySchemes")
	assert.Equal(t, map[string]any{"type": "apiKey", "in": "header", "name": "X-API-Key"},
		schemes["apiKeyHeader"])
	assert.Equal(t, []any{map[string]any{"apiKeyHeader": []any{}}}, spec["security"])
}

package openapi

import (
	"maps"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// resolveSchemaName extracts the schema name from a $ref or returns empty.
func resolveSchemaName(schema map[string]any) string {
	ref := stdx.GetString(schema, keyRef)
	if ref == "" {
		return ""
	}

	parts := strings.Split(ref, textSlash)

	return parts[len(parts)-1]
}

// wrapResponseSchema wraps a protobuf response schema in JSON:API envelope.
func wrapResponseSchema(
	name string,
	schema map[string]any,
	schemas map[string]any,
	cfg serviceConfig,
) map[string]any {
	props := stdx.GetMap(schema, keyProperties)

	// Empty response (e.g. DeleteUserResponse) — no properties key or empty map.
	if len(props) == 0 {
		return map[string]any{
			keyType:        typeObject,
			keyDescription: emptyResponseDesc,
		}
	}

	// Find the resource config by checking if any known resource key is in the
	// properties. Iterate resources in a deterministic (sorted-by-type) order so
	// the output never depends on Go map iteration order. The reader guarantees
	// no two resources in a service share a single/collection key, so at most one
	// resource matches a given response — the sort only fixes the tie-free order.
	resourceTypes := make([]string, 0, len(cfg.resources))
	for t := range cfg.resources {
		resourceTypes = append(resourceTypes, t)
	}

	slices.Sort(resourceTypes)

	for _, t := range resourceTypes {
		secondResourceConfig := cfg.resources[t]

		// Check for collection response (has the collectionKey).
		if _, hasCollection := props[secondResourceConfig.collectionKey]; hasCollection {
			return wrapCollectionResponse(name, secondResourceConfig, schemas)
		}

		// Check for single resource response (has the singleKey).
		if singleProp, hasSingle := props[secondResourceConfig.singleKey]; hasSingle {
			return wrapSingleResponse(
				name,
				singleProp,
				secondResourceConfig,
				schemas,
				props,
			)
		}
	}

	// Non-resource response (e.g. InviteMembersResponse) — wrap as meta-only
	// document per JSON:API spec. Action endpoints return status info, not resources.
	return wrapMetaResponse(name, schemas)
}

// wrapMetaResponse creates a JSON:API meta-only envelope schema for
// non-resource responses (action endpoints that return status info).
func wrapMetaResponse(responseName string, schemas map[string]any) map[string]any {
	metaSchemaName := responseName + "Meta"

	// Build the meta schema from the original response's properties, and point the
	// envelope's `meta` at it by $ref. When the response has NO properties — an
	// action with an empty response message (e.g. PauseSyncResponse), which the
	// ACTION role reaches here directly (bypassing wrapResponseSchema's empty-props
	// early return) — no named schema is emitted, so `meta` falls back to an inline
	// open object rather than a dangling $ref (which breaks a generated client).
	meta := map[string]any{keyType: typeObject}
	respSchema := stdx.GetMap(schemas, responseName)

	if props := stdx.GetMap(respSchema, keyProperties); len(props) > 0 {
		metaProps := make(map[string]any, len(props))
		maps.Copy(metaProps, props)

		metaSchema := map[string]any{
			keyType:       typeObject,
			keyProperties: metaProps,
		}
		// Preserve `required` array from the original response schema.
		if req := stdx.GetSlice(respSchema, keyRequired); len(req) > 0 {
			metaSchema[keyRequired] = req
		}

		schemas[metaSchemaName] = metaSchema
		meta = map[string]any{keyRef: textComponentsSchemas + metaSchemaName}
	}

	// `meta` is required: unlike a single-resource response (where meta only
	// appears when there are extra fields alongside the resource), the runtime
	// TransformMeta always emits a `meta` object for an action endpoint. Marking
	// it required keeps the generated type honest, so clients can read
	// meta.<field> without a defensive optional-chain on `meta`.
	return map[string]any{
		keyType:     typeObject,
		keyRequired: []any{keyMeta},
		keyProperties: map[string]any{
			keyMeta: meta,
		},
	}
}

// wrapSingleResponse creates a JSON:API single-resource envelope schema.
// responseProps contains all properties from the original response schema;
// fields other than the resource key are placed in a `meta` object to match
// the runtime JSON:API middleware behavior (e.g. currentOrgId on GetCurrentUserResponse).
func wrapSingleResponse(
	responseName string,
	singleProp any,
	currentResourceConfig resourceConfig,
	schemas map[string]any,
	responseProps map[string]any,
) map[string]any {
	attrsSchemaName := responseName + "Attributes"
	addResourceAttributes(singleProp, attrsSchemaName, schemas)

	result := map[string]any{
		keyType:     typeObject,
		keyRequired: []any{keyData},
		keyProperties: map[string]any{
			keyData: map[string]any{
				keyType:     typeObject,
				keyRequired: []any{keyType, textID, keyAttributes},
				keyProperties: map[string]any{
					keyType: map[string]any{
						keyType:    keyString,
						keyExample: currentResourceConfig.resourceType,
					},
					textID: map[string]any{
						keyType: keyString,
					},
					keyAttributes: map[string]any{
						keyRef: textComponentsSchemas + attrsSchemaName,
					},
				},
			},
			"links": map[string]any{
				keyType: typeObject,
				keyProperties: map[string]any{
					"self": map[string]any{keyType: keyString},
				},
			},
		},
	}

	addSingleResponseMeta(
		result,
		responseName,
		currentResourceConfig.singleKey,
		schemas,
		responseProps,
	)

	return result
}

// addResourceAttributes registers the attributes schema (the resource minus "id") of
// the resource singleProp references, when it resolves to a known schema.
func addResourceAttributes(
	singleProp any,
	attrsSchemaName string,
	schemas map[string]any,
) {
	sp, ok := singleProp.(map[string]any)
	if !ok {
		return
	}

	name := resolveSchemaName(sp)
	if name == "" {
		return
	}

	if resourceSchema, ok := schemas[name].(map[string]any); ok {
		schemas[attrsSchemaName] = buildAttributesSchema(resourceSchema)
	}
}

// addSingleResponseMeta collects the fields beyond the resource key into a `meta`
// schema, mirroring TransformSingleResource in the runtime middleware.
func addSingleResponseMeta(
	result map[string]any,
	responseName, singleKey string,
	schemas, responseProps map[string]any,
) {
	metaProps := make(map[string]any)

	for k, v := range responseProps {
		if k != singleKey {
			metaProps[k] = v
		}
	}

	if len(metaProps) == 0 {
		return
	}

	metaSchemaName := responseName + "Meta"

	schemas[metaSchemaName] = map[string]any{
		keyType:       typeObject,
		keyProperties: metaProps,
	}

	resultProps := stdx.GetMap(result, keyProperties)

	resultProps[keyMeta] = map[string]any{
		keyRef: textComponentsSchemas + metaSchemaName,
	}
}

// wrapCollectionResponse creates a JSON:API collection envelope schema.
func wrapCollectionResponse(
	responseName string,
	currentResourceConfig resourceConfig,
	schemas map[string]any,
) map[string]any {
	attrsRef := collectionAttributesRef(
		responseName,
		currentResourceConfig.collectionKey,
		schemas,
	)

	return map[string]any{
		keyType:     typeObject,
		keyRequired: []any{keyData},
		keyProperties: map[string]any{
			keyData: map[string]any{
				keyType: "array",
				textItems: map[string]any{
					keyType:     typeObject,
					keyRequired: []any{keyType, textID, keyAttributes},
					keyProperties: map[string]any{
						keyType: map[string]any{
							keyType:    keyString,
							keyExample: currentResourceConfig.resourceType,
						},
						textID: map[string]any{
							keyType: keyString,
						},
						keyAttributes: attrsRef,
					},
				},
			},
			keyMeta: map[string]any{
				keyType: typeObject,
				keyProperties: map[string]any{
					"totalCount": map[string]any{
						keyType:  "integer",
						"format": "int32",
					},
				},
			},
			"links": map[string]any{
				keyType: typeObject,
				keyProperties: map[string]any{
					"self": map[string]any{keyType: keyString},
					"next": map[string]any{keyType: keyString},
				},
			},
		},
	}
}

// collectionAttributesRef registers the attributes schema of a collection's items and
// returns a reference to it. Each accessor yields nil when the shape isn't what we
// expect, so a missing hop falls through to an inline open object.
func collectionAttributesRef(
	responseName, collectionKey string, schemas map[string]any,
) map[string]any {
	collProp := stdx.GetMap(
		stdx.GetMap(stdx.GetMap(schemas, responseName), keyProperties),
		collectionKey,
	)

	itemSchema := stdx.GetMap(
		schemas,
		resolveSchemaName(stdx.GetMap(collProp, textItems)),
	)
	if itemSchema == nil {
		return map[string]any{keyType: typeObject, keyDescription: "Resource attributes"}
	}

	attrsSchemaName := responseName + "ItemAttributes"

	schemas[attrsSchemaName] = buildAttributesSchema(itemSchema)

	return map[string]any{keyRef: textComponentsSchemas + attrsSchemaName}
}

// buildAttributesSchema creates an attributes-only schema by removing the "id" field.
func buildAttributesSchema(resourceSchema map[string]any) map[string]any {
	attrs := map[string]any{
		keyType: typeObject,
	}

	props := stdx.GetMap(resourceSchema, keyProperties)
	if props == nil {
		return attrs
	}

	attrProps := make(map[string]any, len(props))
	for k, v := range props {
		if k != textID {
			attrProps[k] = v
		}
	}

	if len(attrProps) > 0 {
		attrs[keyProperties] = attrProps
	}

	// Preserve `required` array from the resource schema, excluding "id".
	if filtered := requiredWithoutID(resourceSchema); len(filtered) > 0 {
		attrs[keyRequired] = filtered
	}

	return attrs
}

// requiredWithoutID returns the schema's required names, minus "id" and non-strings.
func requiredWithoutID(resourceSchema map[string]any) []any {
	req := stdx.As[[]any](resourceSchema[keyRequired])

	var filtered []any

	for _, r := range req {
		if s, ok := r.(string); ok && s != textID {
			filtered = append(filtered, r)
		}
	}

	return filtered
}

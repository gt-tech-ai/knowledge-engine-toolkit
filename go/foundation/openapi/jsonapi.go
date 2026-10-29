package openapi

import (
	"maps"
	"sort"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/helpers"
)

// rewriteForJSONAPI modifies the OpenAPI 3.0 spec in-place to reflect the
// JSON:API envelope format that clients actually receive. This includes:
//   - Wrapping single-resource response schemas in {data: {type, id, attributes}}
//   - Wrapping collection response schemas in {data: [...], meta: {pagination}}
//   - Enveloping CREATE/UPDATE request bodies in {data: {type, attributes}}
//   - Adding error response schemas in {errors: [...]}
//   - Replacing content type with application/vnd.api+json
//
// The envelope shape is chosen PER OPERATION from its RouteEntry.Role (the single
// generated contract) rather than re-derived by scanning response properties. An
// operation with no RouteEntry (an exempt or unclassified RPC, or when no route table
// is supplied) falls back to the by-resource-property response match, so the exempt
// surface keeps its prior envelope.
func rewriteForJSONAPI(oas map[string]any, cfg serviceConfig, routes []RouteEntry) {
	schemas := getSchemas(oas)
	if schemas == nil {
		return
	}

	// Add shared JSON:API schemas.
	addJSONAPISchemas(schemas)

	// Rewrite response schemas in paths.
	paths := helpers.GetMap(oas, "paths")
	if paths == nil {
		return
	}

	lookup := buildRouteLookup(routes, cfg.basePath)
	for pathKey, pathItem := range paths {
		pi := helpers.AsMap(pathItem)
		if pi == nil {
			continue
		}
		for method, operation := range pi {
			op := helpers.AsMap(operation)
			if op == nil {
				continue
			}
			entry, ok := lookup[routeKey(method, pathKey, cfg.basePath)]
			if ok {
				rewriteOperationByRole(op, schemas, entry)
				continue
			}
			// Exempt/unclassified operation (or no route table): keep the
			// by-resource-property envelope so its contract is unchanged.
			rewriteOperationResponses(op, schemas, cfg)
		}
	}
}

// buildRouteLookup indexes a service's route entries by (upper method, base-path-
// stripped path) so an OpenAPI operation can find its RouteEntry regardless of
// whether the generated path key carries the service base path.
func buildRouteLookup(routes []RouteEntry, basePath string) map[string]RouteEntry {
	lookup := make(map[string]RouteEntry, len(routes))
	for i := range routes {
		lookup[routeKey(routes[i].Method, routes[i].PathTemplate, basePath)] = routes[i]
	}
	return lookup
}

// routeKey normalizes a (method, path) pair into a lookup key: the upper-case verb
// plus the path with any service base-path prefix stripped, trailing slash removed,
// and every "{param}" segment canonicalized to "{}". The canonicalization matters
// because protoc-gen-openapiv2 camelCases path params in the generated OpenAPI keys
// (e.g. "{libraryId}") while RouteEntry.PathTemplate carries the raw
// google.api.http snake_case ("{library_id}"); ignoring the param NAME lets the
// two match on structure, so a route WITH a path param still resolves to its
// RouteEntry (otherwise it silently falls back to the by-property path and misses
// the per-operation request/response enveloping).
func routeKey(method, path, basePath string) string {
	p := path
	if basePath != "" && basePath != "/" && strings.HasPrefix(p, basePath) {
		p = strings.TrimPrefix(p, basePath)
	}
	if p == "" {
		p = "/"
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return strings.ToUpper(method) + " " + canonicalizePathParams(p)
}

// canonicalizePathParams rewrites every "{param}" path segment to "{}" so a path
// template matches regardless of the (camelCase vs snake_case) parameter name.
func canonicalizePathParams(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

// rewriteOperationByRole rewrites an operation's responses (and, for a write op, its
// request body) into the JSON:API envelope dictated by its RouteEntry.Role. It (1)
// reshapes the success (2xx) response body into the role's envelope, (2) renumbers the
// success status to the code the JSON:API gateway actually returns for that role —
// CREATE→201, DELETE→204 (no body), everything else 200 (mirroring
// knowledge-engine-toolkit/go/transport/jsonapi.middleware's TransformResponse) — and (3)
// replaces the generic transcoder error with the role-derived set of coded error
// responses (errorStatusesForRole), each carrying the JSON:API error envelope. The base
// grpc-gateway swagger emits every success as 200 and only a `default` error, so without
// (2)+(3) the generated spec (and the clients built from it) reported the wrong status
// and no coded errors.
func rewriteOperationByRole(op, schemas map[string]any, entry RouteEntry) {
	responses := helpers.GetMap(op, "responses")
	if responses == nil {
		return
	}

	rewriteSuccessResponse(responses, op, schemas, entry)

	// Replace every inherited error/`default` response with the role-derived coded
	// set, each in the JSON:API error envelope.
	for code := range responses {
		if code == "default" || (len(code) == 3 && code[0] >= '4') {
			delete(responses, code)
		}
	}
	for _, code := range errorStatusesForRole(entry.Role, entry.PathTemplate) {
		responses[code] = jsonAPIErrorResponse(code)
	}

	envelopeRequestBody(op, schemas, entry)
}

// rewriteSuccessResponse reshapes the operation's success (2xx) response into the
// role's JSON:API envelope and moves it under the status code the gateway returns
// for that role: a DELETE becomes a bodyless 204, a CREATE a 201 carrying the
// created-resource envelope, and every read/update/action stays 200.
func rewriteSuccessResponse(responses, op, schemas map[string]any, entry RouteEntry) {
	var srcCode string
	var srcResp map[string]any
	for code, resp := range responses {
		if len(code) == 3 && code[0] == '2' {
			if r := helpers.AsMap(resp); r != nil {
				srcCode, srcResp = code, r
				break
			}
		}
	}
	if srcResp == nil {
		return
	}

	target := successStatusForRole(entry.Role)

	// DELETE: a 204 with no body (the gateway writes StatusNoContent, no envelope).
	if entry.Role == RoleDelete {
		delete(responses, srcCode)
		responses[target] = map[string]any{keyDescription: "No Content"}
		return
	}

	// Reshape the body into the role's envelope (JSON:API content type).
	if content := helpers.GetMap(srcResp, "content"); content != nil &&
		helpers.GetMap(content, mediaJSON) != nil {
		if wrapped := wrapByRole(op, schemas, entry); wrapped != nil {
			delete(content, mediaJSON)
			content["application/vnd.api+json"] = map[string]any{keySchema: wrapped}
		}
	}

	// Renumber when the role's success code differs from the inherited one (CREATE).
	if target != srcCode {
		delete(responses, srcCode)
		responses[target] = srcResp
	}
}

// successStatusForRole is the HTTP status the JSON:API gateway returns on success
// for a role: CREATE→201, DELETE→204, and 200 for every read/update/action.
func successStatusForRole(role Role) string {
	switch role {
	case RoleCreate:
		return "201"
	case RoleDelete:
		return "204"
	case RoleList, RoleGetOne, RoleUpdate, RoleAction:
		return "200"
	default:
		return "200"
	}
}

// errorStatusesForRole is the set of coded error statuses an operation of the given
// role can return, derived from the connect-code→HTTP mapping the runtime uses:
// every authenticated REST op can fail validation (400), authentication (401), and
// authorization (403); an op addressing a resource or parent by id (a "{param}" in
// its path) can 404; and a create/update can conflict (409, e.g. a uniqueness
// violation). Per-RPC exactness (e.g. an action that returns 409) is left to a
// future per-RPC error annotation. Sorted for deterministic spec output.
func errorStatusesForRole(role Role, pathTemplate string) []string {
	codes := []string{"400", "401", "403"}
	if pathHasParam(pathTemplate) {
		codes = append(codes, "404")
	}
	if role == RoleCreate || role == RoleUpdate {
		codes = append(codes, "409")
	}
	sort.Strings(codes)
	return codes
}

// pathHasParam reports whether a path template addresses a specific resource or
// parent by id (carries a "{param}" segment) — the operations that can 404.
func pathHasParam(pathTemplate string) bool {
	return strings.Contains(pathTemplate, "{")
}

// jsonAPIErrorResponse builds one coded error response in the JSON:API error
// envelope, described by its status.
func jsonAPIErrorResponse(status string) map[string]any {
	return map[string]any{
		keyDescription: errorStatusDescription(status),
		"content": map[string]any{
			"application/vnd.api+json": map[string]any{
				keySchema: map[string]any{
					keyRef: "#/components/schemas/JsonApiErrorResponse",
				},
			},
		},
	}
}

// errorStatusDescription is the human-readable reason for a coded error status.
func errorStatusDescription(status string) string {
	switch status {
	case "400":
		return "Invalid request (validation or request-binding failure)."
	case "401":
		return "Missing or invalid authentication."
	case "403":
		return "The caller is not permitted to perform this operation."
	case "404":
		return "The addressed resource does not exist."
	case "409":
		return "The request conflicts with the current resource state."
	default:
		return "Error response."
	}
}

// wrapByRole builds the success-response envelope schema for an operation's role,
// reusing the single/collection/meta wrappers so the shape is identical to the
// by-property path for LIST/GET_ONE/CREATE/UPDATE (parity) while making ACTION
// meta-only and DELETE body-less deterministically:
//   - LIST                       → {data: [...], meta}
//   - GET_ONE/UPDATE, and CREATE → {data: {type, id, attributes}} (or a
//     collection for a bulk CREATE whose response carries only a collection key)
//   - ACTION                     → {meta: {...}} (never a data resource)
//   - DELETE                     → an empty object (no resource body)
func wrapByRole(op, schemas map[string]any, entry RouteEntry) map[string]any {
	rc := resourceConfig{
		resourceType:  entry.ResourceType,
		singleKey:     entry.SingleKey,
		collectionKey: entry.CollectionKey,
		paginationKey: entry.PaginationKey,
	}

	switch entry.Role {
	case RoleDelete:
		return map[string]any{keyType: typeObject, keyDescription: emptyResponseDesc}
	case RoleAction:
		return wrapMetaResponse(responseSchemaName(op), schemas)
	case RoleList:
		return wrapCollectionResponse(responseSchemaName(op), rc, schemas)
	case RoleGetOne, RoleCreate, RoleUpdate:
		name := responseSchemaName(op)
		respSchema := helpers.GetMap(schemas, name)
		props := helpers.GetMap(respSchema, keyProperties)
		// A bulk create whose response carries only a collection key is still a
		// collection envelope (the CREATE rule).
		if rc.singleKey == "" && rc.collectionKey != "" {
			return wrapCollectionResponse(name, rc, schemas)
		}
		if len(props) == 0 {
			return map[string]any{keyType: typeObject, keyDescription: emptyResponseDesc}
		}
		// A resource-less GET_ONE/UPDATE (classified from verb/path, no resource
		// key on its own response) wraps meta-only — matching the by-property
		// fallback — rather than emitting a dangling attributes $ref.
		if rc.singleKey == "" {
			return wrapMetaResponse(name, schemas)
		}
		return wrapSingleResponse(name, props[rc.singleKey], rc, schemas, props)
	default: // GET_ONE / CREATE / UPDATE
		name := responseSchemaName(op)
		respSchema := helpers.GetMap(schemas, name)
		props := helpers.GetMap(respSchema, keyProperties)
		// A bulk create whose response carries only a collection key is still a
		// collection envelope (the CREATE rule).
		if rc.singleKey == "" && rc.collectionKey != "" {
			return wrapCollectionResponse(name, rc, schemas)
		}
		if len(props) == 0 {
			return map[string]any{keyType: typeObject, keyDescription: emptyResponseDesc}
		}
		// A resource-less GET_ONE/UPDATE (classified from verb/path, no resource
		// key on its own response) wraps meta-only — matching the by-property
		// fallback — rather than emitting a dangling attributes $ref.
		if rc.singleKey == "" {
			return wrapMetaResponse(name, schemas)
		}
		return wrapSingleResponse(name, props[rc.singleKey], rc, schemas, props)
	}
}

// responseSchemaName resolves the schema name of an operation's success (2xx)
// response body, or "" when none is present.
func responseSchemaName(op map[string]any) string {
	responses := helpers.GetMap(op, "responses")
	for code, resp := range responses {
		if len(code) != 3 || code[0] != '2' {
			continue
		}
		schema := helpers.GetMap(
			helpers.GetMap(
				helpers.GetMap(helpers.AsMap(resp), "content"),
				mediaJSON,
			),
			keySchema,
		)
		if name := resolveSchemaName(schema); name != "" {
			return name
		}
	}
	return ""
}

// envelopeRequestBody rewrites a write operation's request body into the JSON:API
// {data: {type, attributes}} envelope (CREATE and UPDATE only). The request
// attributes schema is the request message minus id, mirroring the response
// attributes extraction.
func envelopeRequestBody(op, schemas map[string]any, entry RouteEntry) {
	if !entry.RequestEnvelope {
		return
	}
	content := helpers.GetMap(helpers.GetMap(op, "requestBody"), "content")
	if content == nil {
		return
	}
	schema := helpers.GetMap(helpers.GetMap(content, mediaJSON), keySchema)
	reqName := resolveSchemaName(schema)
	if reqName == "" {
		return
	}

	attrsName := reqName + "Attributes"
	if reqSchema := helpers.GetMap(schemas, reqName); reqSchema != nil {
		schemas[attrsName] = buildAttributesSchema(reqSchema)
	}

	delete(content, mediaJSON)
	content["application/vnd.api+json"] = map[string]any{
		keySchema: map[string]any{
			keyType:     typeObject,
			keyRequired: []any{keyData},
			keyProperties: map[string]any{
				keyData: map[string]any{
					keyType:     typeObject,
					keyRequired: []any{keyType, keyAttributes},
					keyProperties: map[string]any{
						keyType: map[string]any{
							keyType:    keyString,
							keyExample: entry.ResourceType,
						},
						keyAttributes: map[string]any{
							keyRef: "#/components/schemas/" + attrsName,
						},
					},
				},
			},
		},
	}
}

// getSchemas returns the components/schemas map, creating it if necessary.
func getSchemas(oas map[string]any) map[string]any {
	components := helpers.GetMap(oas, "components")
	if components == nil {
		return nil
	}
	schemas := helpers.GetMap(components, "schemas")
	if schemas == nil {
		schemas = map[string]any{}
		components["schemas"] = schemas
	}
	return schemas
}

// addJSONAPISchemas injects shared JSON:API structural schemas.
//
// The error member mirrors what
// knowledge-engine-toolkit/go/transport/jsonapi.TransformError emits: "code"
// (machine-readable app error code) and "status" (the HTTP status as a string) are always
// present, while "detail" (the human-readable message) is omitted when the upstream error
// carries none — so it is the only optional member. "errors" itself is required: JSON:API
// models it as an array, and the transformer always writes exactly one element.
func addJSONAPISchemas(schemas map[string]any) {
	schemas["JsonApiError"] = map[string]any{
		keyType:     typeObject,
		keyRequired: []any{"code", "status"},
		keyProperties: map[string]any{
			"code": map[string]any{
				keyType:    keyString,
				keyExample: "not_found",
			},
			"status": map[string]any{
				keyType:    keyString,
				keyExample: "404",
			},
			"detail": map[string]any{
				keyType:    keyString,
				keyExample: "user not found",
			},
		},
	}

	schemas["JsonApiErrorResponse"] = map[string]any{
		keyType:     typeObject,
		keyRequired: []any{"errors"},
		keyProperties: map[string]any{
			"errors": map[string]any{
				keyType: "array",
				"items": map[string]any{
					keyRef: "#/components/schemas/JsonApiError",
				},
			},
		},
	}
}

// rewriteOperationResponses rewrites response content types and schemas for JSON:API.
func rewriteOperationResponses(op, schemas map[string]any, cfg serviceConfig) {
	responses := helpers.GetMap(op, "responses")
	if responses == nil {
		return
	}

	for code, resp := range responses {
		r := helpers.AsMap(resp)
		if r == nil {
			continue
		}

		content := helpers.GetMap(r, "content")
		if content == nil {
			continue
		}

		jsonContent := helpers.GetMap(content, mediaJSON)
		if jsonContent == nil {
			continue
		}

		// Error responses get JSON:API error envelope.
		if code == "default" || (len(code) == 3 && code[0] >= '4') {
			delete(content, mediaJSON)
			content["application/vnd.api+json"] = map[string]any{
				keySchema: map[string]any{
					keyRef: "#/components/schemas/JsonApiErrorResponse",
				},
			}
			continue
		}

		// Success responses: determine if single or collection.
		schema := helpers.GetMap(jsonContent, keySchema)
		if schema == nil {
			continue
		}

		schemaName := resolveSchemaName(schema)
		if schemaName == "" {
			continue
		}

		// Check if it's a collection or single response based on the schema content.
		actualSchema := helpers.GetMap(schemas, schemaName)
		if actualSchema == nil {
			continue
		}

		wrapped := wrapResponseSchema(schemaName, actualSchema, schemas, cfg)
		if wrapped != nil {
			delete(content, mediaJSON)
			content["application/vnd.api+json"] = map[string]any{
				keySchema: wrapped,
			}
		}
	}
}

// resolveSchemaName extracts the schema name from a $ref or returns empty.
func resolveSchemaName(schema map[string]any) string {
	ref := helpers.GetString(schema, keyRef)
	if ref == "" {
		return ""
	}
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

// wrapResponseSchema wraps a protobuf response schema in JSON:API envelope.
func wrapResponseSchema(
	name string,
	schema map[string]any,
	schemas map[string]any,
	cfg serviceConfig,
) map[string]any {
	props := helpers.GetMap(schema, keyProperties)

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
	sort.Strings(resourceTypes)
	for _, t := range resourceTypes {
		rc := cfg.resources[t]

		// Check for collection response (has the collectionKey).
		if _, hasCollection := props[rc.collectionKey]; hasCollection {
			return wrapCollectionResponse(name, rc, schemas)
		}

		// Check for single resource response (has the singleKey).
		if singleProp, hasSingle := props[rc.singleKey]; hasSingle {
			return wrapSingleResponse(name, singleProp, rc, schemas, props)
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
	metaValue := map[string]any{keyType: typeObject}
	respSchema := helpers.GetMap(schemas, responseName)
	if props := helpers.GetMap(respSchema, keyProperties); len(props) > 0 {
		metaProps := make(map[string]any, len(props))
		maps.Copy(metaProps, props)
		metaSchema := map[string]any{
			keyType:       typeObject,
			keyProperties: metaProps,
		}
		// Preserve `required` array from the original response schema.
		if req := helpers.GetSlice(respSchema, keyRequired); len(req) > 0 {
			metaSchema[keyRequired] = req
		}
		schemas[metaSchemaName] = metaSchema
		metaValue = map[string]any{keyRef: "#/components/schemas/" + metaSchemaName}
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
			keyMeta: metaValue,
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
	rc resourceConfig,
	schemas map[string]any,
	responseProps map[string]any,
) map[string]any {
	// Resolve the resource schema reference.
	resourceSchemaName := ""
	if sp, ok := singleProp.(map[string]any); ok {
		resourceSchemaName = resolveSchemaName(sp)
	}

	// Create an attributes schema by extracting the resource schema minus "id".
	attrsSchemaName := responseName + "Attributes"
	if resourceSchemaName != "" {
		if resourceSchema, ok := schemas[resourceSchemaName].(map[string]any); ok {
			attrsSchema := buildAttributesSchema(resourceSchema)
			schemas[attrsSchemaName] = attrsSchema
		}
	}

	result := map[string]any{
		keyType:     typeObject,
		keyRequired: []any{keyData},
		keyProperties: map[string]any{
			keyData: map[string]any{
				keyType:     typeObject,
				keyRequired: []any{keyType, "id", keyAttributes},
				keyProperties: map[string]any{
					keyType: map[string]any{
						keyType:    keyString,
						keyExample: rc.resourceType,
					},
					"id": map[string]any{
						keyType: keyString,
					},
					keyAttributes: map[string]any{
						keyRef: "#/components/schemas/" + attrsSchemaName,
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

	// Collect extra response fields (beyond the resource key) into a `meta`
	// schema. This mirrors TransformSingleResource in the runtime middleware,
	// which puts fields like currentOrgId into the meta envelope.
	metaProps := make(map[string]any)
	for k, v := range responseProps {
		if k == rc.singleKey {
			continue
		}
		metaProps[k] = v
	}
	if len(metaProps) > 0 {
		metaSchemaName := responseName + "Meta"
		schemas[metaSchemaName] = map[string]any{
			keyType:       typeObject,
			keyProperties: metaProps,
		}
		resultProps := helpers.GetMap(result, keyProperties)
		resultProps[keyMeta] = map[string]any{
			keyRef: "#/components/schemas/" + metaSchemaName,
		}
	}

	return result
}

// wrapCollectionResponse creates a JSON:API collection envelope schema.
func wrapCollectionResponse(
	responseName string,
	rc resourceConfig,
	schemas map[string]any,
) map[string]any {
	// Try to find or create the attributes schema from the items.
	attrsRef := map[string]any{
		keyType:        typeObject,
		keyDescription: "Resource attributes",
	}
	attrsSchemaName := responseName + "ItemAttributes"

	// Look for the items schema in the collection definition. Each accessor
	// yields nil when the shape isn't what we expect, so a missing hop leaves
	// itemSchemaName empty and attrsRef at its default — the same fall-through
	// the nested type assertions produced, without the five-deep pyramid.
	collProp := helpers.GetMap(
		helpers.GetMap(helpers.GetMap(schemas, responseName), keyProperties),
		rc.collectionKey,
	)
	items := helpers.GetMap(collProp, "items")
	if itemSchemaName := resolveSchemaName(items); itemSchemaName != "" {
		if itemSchema := helpers.GetMap(schemas, itemSchemaName); itemSchema != nil {
			schemas[attrsSchemaName] = buildAttributesSchema(itemSchema)
			attrsRef = map[string]any{
				keyRef: "#/components/schemas/" + attrsSchemaName,
			}
		}
	}

	return map[string]any{
		keyType:     typeObject,
		keyRequired: []any{keyData},
		keyProperties: map[string]any{
			keyData: map[string]any{
				keyType: "array",
				"items": map[string]any{
					keyType:     typeObject,
					keyRequired: []any{keyType, "id", keyAttributes},
					keyProperties: map[string]any{
						keyType: map[string]any{
							keyType:    keyString,
							keyExample: rc.resourceType,
						},
						"id": map[string]any{
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

// buildAttributesSchema creates an attributes-only schema by removing the "id" field.
func buildAttributesSchema(resourceSchema map[string]any) map[string]any {
	attrs := map[string]any{
		keyType: typeObject,
	}

	props := helpers.GetMap(resourceSchema, keyProperties)
	if props == nil {
		return attrs
	}

	attrProps := make(map[string]any, len(props))
	for k, v := range props {
		if k == "id" {
			continue
		}
		attrProps[k] = v
	}
	if len(attrProps) > 0 {
		attrs[keyProperties] = attrProps
	}

	// Preserve `required` array from the resource schema, excluding "id".
	if req, ok := resourceSchema[keyRequired].([]any); ok {
		var filtered []any
		for _, r := range req {
			if s, ok := r.(string); ok && s != "id" {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) > 0 {
			attrs[keyRequired] = filtered
		}
	}

	return attrs
}

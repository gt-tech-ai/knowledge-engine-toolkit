package openapi

import (
	"maps"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
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
	paths := stdx.GetMap(oas, "paths")
	if paths == nil {
		return
	}

	lookup := buildRouteLookup(routes, cfg.basePath)

	for pathKey, pathItem := range paths {
		rewritePathItem(pathKey, stdx.AsMap(pathItem), schemas, cfg, lookup)
	}
}

// rewritePathItem rewrites each operation of one path; a non-object entry is skipped.
func rewritePathItem(
	pathKey string,
	item, schemas map[string]any,
	cfg serviceConfig,
	lookup map[string]RouteEntry,
) {
	for method, operation := range item {
		if op := stdx.AsMap(operation); op != nil {
			rewriteOperation(
				op,
				schemas,
				cfg,
				lookup,
				routeKey(method, pathKey, cfg.basePath),
			)
		}
	}
}

// rewriteOperation envelopes one operation by its route role; an exempt or
// unclassified operation (or no route table) keeps the by-resource-property
// envelope so its contract is unchanged.
func rewriteOperation(
	operation, schemas map[string]any,
	cfg serviceConfig,
	lookup map[string]RouteEntry,
	key string,
) {
	entry, ok := lookup[key]
	if ok {
		rewriteOperationByRole(operation, schemas, &entry)

		return
	}

	rewriteOperationResponses(operation, schemas, cfg)
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
	text := path
	if basePath != "" && basePath != textSlash && strings.HasPrefix(text, basePath) {
		text = strings.TrimPrefix(text, basePath)
	}

	if text == "" {
		text = textSlash
	}

	if len(text) > 1 {
		text = strings.TrimRight(text, textSlash)
	}

	return strings.ToUpper(method) + " " + canonicalizePathParams(text)
}

// canonicalizePathParams rewrites every "{param}" path segment to "{}" so a path
// template matches regardless of the (camelCase vs snake_case) parameter name.
func canonicalizePathParams(path string) string {
	segs := strings.Split(path, textSlash)
	for i, s := range segs {
		if len(s) >= minPathParamLen && s[0] == '{' && s[len(s)-1] == '}' {
			segs[i] = "{}"
		}
	}

	return strings.Join(segs, textSlash)
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
func rewriteOperationByRole(operation, schemas map[string]any, entry *RouteEntry) {
	responses := stdx.GetMap(operation, textResponses)
	if responses == nil {
		return
	}

	rewriteSuccessResponse(responses, operation, schemas, entry)

	// Replace every inherited error/`default` response with the role-derived coded
	// set, each in the JSON:API error envelope.
	for code := range responses {
		if isErrorStatus(code) {
			delete(responses, code)
		}
	}

	for _, code := range errorStatusesForRole(entry.Role, entry.PathTemplate) {
		responses[code] = jsonAPIErrorResponse(code)
	}

	envelopeRequestBody(operation, schemas, entry)
}

// isErrorStatus reports whether a response key is `default` or a 4xx/5xx status.
func isErrorStatus(code string) bool {
	return code == "default" || (len(code) == statusCodeLen && code[0] >= '4')
}

// isSuccessStatus reports whether a response key is a 2xx status.
func isSuccessStatus(code string) bool {
	return len(code) == statusCodeLen && code[0] == '2'
}

// findSuccessResponse returns the first 2xx response that is a schema object.
func findSuccessResponse(
	responses map[string]any,
) (status string, response map[string]any) {
	for code, resp := range responses {
		if r := stdx.AsMap(resp); isSuccessStatus(code) && r != nil {
			return code, r
		}
	}

	return "", nil
}

// rewriteSuccessResponse reshapes the operation's success (2xx) response into the
// role's JSON:API envelope and moves it under the status code the gateway returns
// for that role: a DELETE becomes a bodyless 204, a CREATE a 201 carrying the
// created-resource envelope, and every read/update/action stays 200.
func rewriteSuccessResponse(
	responses, operation, schemas map[string]any,
	entry *RouteEntry,
) {
	srcCode, srcResp := findSuccessResponse(responses)
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

	reshapeSuccessBody(srcResp, operation, schemas, entry)

	// Renumber when the role's success code differs from the inherited one (CREATE).
	if target != srcCode {
		delete(responses, srcCode)

		responses[target] = srcResp
	}
}

// reshapeSuccessBody replaces a JSON body with the role's envelope under the JSON:API
// content type, leaving a response with no JSON body or no envelope untouched.
func reshapeSuccessBody(resp, operation, schemas map[string]any, entry *RouteEntry) {
	content := stdx.GetMap(resp, textContent)
	if content == nil || stdx.GetMap(content, mediaJSON) == nil {
		return
	}

	wrapped := wrapByRole(operation, schemas, entry)
	if wrapped == nil {
		return
	}

	delete(content, mediaJSON)

	content[textApplicationVndAPIJSON] = map[string]any{keySchema: wrapped}
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
		// Any other value takes the shared result below.
	}

	return "200"
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
		codes = append(codes, text404)
	}

	if role == RoleCreate || role == RoleUpdate {
		codes = append(codes, "409")
	}

	slices.Sort(codes)

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
		textContent: map[string]any{
			textApplicationVndAPIJSON: map[string]any{
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
	case text404:
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
func wrapByRole(operation, schemas map[string]any, entry *RouteEntry) map[string]any {
	currentResourceConfig := resourceConfig{
		resourceType:  entry.ResourceType,
		singleKey:     entry.SingleKey,
		collectionKey: entry.CollectionKey,
		paginationKey: entry.PaginationKey,
	}

	name := responseSchemaName(operation)

	switch entry.Role {
	case RoleDelete:
		return map[string]any{keyType: typeObject, keyDescription: emptyResponseDesc}
	case RoleAction:
		return wrapMetaResponse(name, schemas)
	case RoleList:
		return wrapCollectionResponse(name, currentResourceConfig, schemas)
	case RoleGetOne, RoleCreate, RoleUpdate:
		return wrapResourceResponse(name, currentResourceConfig, schemas)
	default:
		// Any other value takes the shared result below.
	}

	return wrapResourceResponse(name, currentResourceConfig, schemas)
}

// wrapResourceResponse builds the envelope of a GET_ONE, CREATE or UPDATE response.
func wrapResourceResponse(
	name string,
	currentResourceConfig resourceConfig,
	schemas map[string]any,
) map[string]any {
	props := stdx.GetMap(stdx.GetMap(schemas, name), keyProperties)

	// A bulk create whose response carries only a collection key is still a
	// collection envelope (the CREATE rule).
	if currentResourceConfig.singleKey == "" &&
		currentResourceConfig.collectionKey != "" {
		return wrapCollectionResponse(name, currentResourceConfig, schemas)
	}

	if len(props) == 0 {
		return map[string]any{keyType: typeObject, keyDescription: emptyResponseDesc}
	}

	// A resource-less GET_ONE/UPDATE (classified from verb/path, no resource
	// key on its own response) wraps meta-only — matching the by-property
	// fallback — rather than emitting a dangling attributes $ref.
	if currentResourceConfig.singleKey == "" {
		return wrapMetaResponse(name, schemas)
	}

	return wrapSingleResponse(
		name,
		props[currentResourceConfig.singleKey],
		currentResourceConfig,
		schemas,
		props,
	)
}

// responseSchemaName resolves the schema name of an operation's success (2xx)
// response body, or "" when none is present.
func responseSchemaName(op map[string]any) string {
	responses := stdx.GetMap(op, textResponses)
	for code, resp := range responses {
		if !isSuccessStatus(code) {
			continue
		}

		schema := stdx.GetMap(
			stdx.GetMap(
				stdx.GetMap(stdx.AsMap(resp), textContent),
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
func envelopeRequestBody(op, schemas map[string]any, entry *RouteEntry) {
	if !entry.RequestEnvelope {
		return
	}

	content := stdx.GetMap(stdx.GetMap(op, "requestBody"), textContent)
	if content == nil {
		return
	}

	schema := stdx.GetMap(stdx.GetMap(content, mediaJSON), keySchema)
	reqName := resolveSchemaName(schema)

	if reqName == "" {
		return
	}

	attrsName := reqName + "Attributes"
	if reqSchema := stdx.GetMap(schemas, reqName); reqSchema != nil {
		schemas[attrsName] = buildAttributesSchema(reqSchema)
	}

	delete(content, mediaJSON)

	content[textApplicationVndAPIJSON] = map[string]any{
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
							keyRef: textComponentsSchemas + attrsName,
						},
					},
				},
			},
		},
	}
}

// getSchemas returns the components/schemas map, creating it if necessary.
func getSchemas(oas map[string]any) map[string]any {
	components := stdx.GetMap(oas, "components")
	if components == nil {
		return nil
	}

	schemas := stdx.GetMap(components, "schemas")
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
				keyExample: text404,
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
				textItems: map[string]any{
					keyRef: "#/components/schemas/JsonApiError",
				},
			},
		},
	}
}

// rewriteOperationResponses rewrites response content types and schemas for JSON:API.
func rewriteOperationResponses(op, schemas map[string]any, cfg serviceConfig) {
	for code, resp := range stdx.GetMap(op, textResponses) {
		content := stdx.GetMap(stdx.AsMap(resp), textContent)

		jsonContent := stdx.GetMap(content, mediaJSON)
		if jsonContent == nil {
			continue
		}

		envelope := responseEnvelope(code, jsonContent, schemas, cfg)
		if envelope == nil {
			continue
		}

		delete(content, mediaJSON)

		content[textApplicationVndAPIJSON] = map[string]any{keySchema: envelope}
	}
}

// responseEnvelope returns the schema that replaces one JSON response body: the
// shared error envelope for an error status, the resource envelope otherwise, or nil
// when the body names no known schema and must stay as it is.
func responseEnvelope(
	code string, jsonContent, schemas map[string]any, cfg serviceConfig,
) map[string]any {
	if isErrorStatus(code) {
		return map[string]any{keyRef: "#/components/schemas/JsonApiErrorResponse"}
	}

	schemaName := resolveSchemaName(stdx.GetMap(jsonContent, keySchema))
	if schemaName == "" {
		return nil
	}

	actualSchema := stdx.GetMap(schemas, schemaName)
	if actualSchema == nil {
		return nil
	}

	return wrapResponseSchema(schemaName, actualSchema, schemas, cfg)
}

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

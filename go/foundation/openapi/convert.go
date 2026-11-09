package openapi

import (
	"encoding/json"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
)

// convertToOAS3 converts a merged Swagger 2.0 spec to OpenAPI 3 through kin-openapi's
// openapi2conv and returns it as a JSON map for the JSON:API rewrite. It adds a server
// at the service's base path when the spec names no host, a default info block, and one
// API-key security scheme per entry of headers (scheme name → request header), all
// required on every operation; empty headers declare no security.
func convertToOAS3(
	swagger map[string]any, cfg serviceConfig, headers map[string]string,
) (map[string]any, error) {
	secondDoc, err := parseSwagger2(swagger)
	if err != nil {
		return nil, err
	}

	oas, err := swagger2ToMap(secondDoc)
	if err != nil {
		return nil, err
	}

	if defs, ok := swagger["definitions"].(map[string]any); ok {
		schemas := stdx.As[map[string]any](stdx.GetMap(oas, "components")["schemas"])
		for name, def := range defs {
			graftRefSiblings(def, schemas[name])
		}
	}

	if _, ok := swagger["info"]; !ok {
		oas["info"] = map[string]any{"title": cfg.service + " API", "version": "1.0.0"}
	}

	if _, ok := oas["servers"]; !ok {
		oas["servers"] = []any{map[string]any{"url": cfg.basePath}}
	}

	addSecuritySchemes(oas, headers)

	return oas, nil
}

// parseSwagger2 decodes the merged spec and defaults its media types:
// protoc-gen-openapiv2 declares JSON, and a spec that omits it would convert to */*.
func parseSwagger2(swagger map[string]any) (*openapi2.T, error) {
	raw, err := json.Marshal(swagger)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "marshal swagger")
	}

	var secondDoc openapi2.T

	err = json.Unmarshal(raw, &secondDoc)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse swagger")
	}

	if len(secondDoc.Consumes) == 0 {
		secondDoc.Consumes = []string{mediaJSON}
	}

	if len(secondDoc.Produces) == 0 {
		secondDoc.Produces = []string{mediaJSON}
	}

	return &secondDoc, nil
}

// swagger2ToMap converts through openapi2conv and returns the result as a JSON map
// for the JSON:API rewrite.
func swagger2ToMap(secondDoc *openapi2.T) (map[string]any, error) {
	thirdDoc, err := openapi2conv.ToV3(secondDoc)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"convert swagger to openapi 3",
		)
	}

	raw, err := json.Marshal(thirdDoc)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "marshal openapi 3")
	}

	var oas map[string]any

	err = json.Unmarshal(raw, &oas)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "decode openapi 3")
	}

	return oas, nil
}

// addSecuritySchemes declares one API-key scheme per entry of headers (scheme name
// → request header), all required on every operation; empty headers declare none.
func addSecuritySchemes(oas map[string]any, headers map[string]string) {
	if len(headers) == 0 {
		return
	}

	components := stdx.As[map[string]any](oas["components"])
	if components == nil {
		components = map[string]any{}
		oas["components"] = components
	}

	schemes := make(map[string]any, len(headers))
	required := make(map[string]any, len(headers))

	for scheme, header := range headers {
		schemes[scheme] = map[string]any{
			keyType: keyAPIKey,
			"in":    keyHeader,
			keyName: header,
		}
		required[scheme] = []any{}
	}

	components["securitySchemes"] = schemes
	oas["security"] = []any{required}
}

// graftRefSiblings copies back what openapi2conv drops: the keys beside a $ref in the
// Swagger schema from (a field's description, readOnly, title), which OpenAPI 3.0
// ignores but Redoc and openapi-typescript render (as docs and `readonly`). to is the
// converted schema at the same place; the conversion keeps the schema's structure, so
// the two are walked in step.
func graftRefSiblings(from, to any) {
	switch f := from.(type) {
	case map[string]any:
		graftMapSiblings(f, to)
	case []any:
		graftListSiblings(f, to)
	default:
		// No other value changes the outcome.
	}
}

// graftMapSiblings copies a $ref's sibling keys, or recurses into a plain schema.
func graftMapSiblings(from map[string]any, to any) {
	lookup, ok := to.(map[string]any)
	if !ok {
		return
	}

	if _, isRef := from[keyRef]; !isRef {
		for k, v := range from {
			graftRefSiblings(v, lookup[k])
		}

		return
	}

	for k, v := range from {
		if _, set := lookup[k]; !set {
			lookup[k] = v
		}
	}
}

// graftListSiblings walks two lists in step, leaving a differently shaped list alone.
func graftListSiblings(from []any, to any) {
	items, ok := to.([]any)
	if !ok || len(items) != len(from) {
		return
	}

	for i := range from {
		graftRefSiblings(from[i], items[i])
	}
}

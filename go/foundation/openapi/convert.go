package openapi

import (
	"encoding/json"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/helpers"
)

// convertToOAS3 converts a merged Swagger 2.0 spec to OpenAPI 3 through kin-openapi's
// openapi2conv and returns it as a JSON map for the JSON:API rewrite. It adds a server
// at the service's base path when the spec names no host, a default info block, and one
// API-key security scheme per entry of headers (scheme name → request header), all
// required on every operation; empty headers declare no security.
func convertToOAS3(
	swagger map[string]any, cfg serviceConfig, headers map[string]string,
) (map[string]any, error) {
	raw, err := json.Marshal(swagger)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "marshal swagger")
	}
	var doc2 openapi2.T
	if err := json.Unmarshal(raw, &doc2); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse swagger")
	}
	// protoc-gen-openapiv2 declares JSON; a spec that omits it would convert to */*.
	if len(doc2.Consumes) == 0 {
		doc2.Consumes = []string{mediaJSON}
	}
	if len(doc2.Produces) == 0 {
		doc2.Produces = []string{mediaJSON}
	}
	doc3, err := openapi2conv.ToV3(&doc2)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"convert swagger to openapi 3",
		)
	}
	if raw, err = json.Marshal(doc3); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "marshal openapi 3")
	}
	var oas map[string]any
	if err := json.Unmarshal(raw, &oas); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "decode openapi 3")
	}

	if defs, ok := swagger["definitions"].(map[string]any); ok {
		schemas, _ := helpers.GetMap(oas, "components")["schemas"].(map[string]any)
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
	if len(headers) == 0 {
		return oas, nil
	}
	components, _ := oas["components"].(map[string]any)
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
	return oas, nil
}

// graftRefSiblings copies back what openapi2conv drops: the keys beside a $ref in the
// Swagger schema from (a field's description, readOnly, title), which OpenAPI 3.0
// ignores but Redoc and openapi-typescript render (as docs and `readonly`). to is the
// converted schema at the same place; the conversion keeps the schema's structure, so
// the two are walked in step.
func graftRefSiblings(from, to any) {
	switch f := from.(type) {
	case map[string]any:
		t, ok := to.(map[string]any)
		if !ok {
			return
		}
		if _, isRef := f[keyRef]; isRef {
			for k, v := range f {
				if _, set := t[k]; !set {
					t[k] = v
				}
			}
			return
		}
		for k, v := range f {
			graftRefSiblings(v, t[k])
		}
	case []any:
		t, ok := to.([]any)
		if !ok || len(t) != len(f) {
			return
		}
		for i := range f {
			graftRefSiblings(f[i], t[i])
		}
	}
}

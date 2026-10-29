package openapi

// OpenAPI / JSON:API document keys and shared literal values used by the converter and
// the JSON:API response builder.
const (
	keyRef         = "$ref"
	keySchema      = "schema"
	keyDescription = "description"
	keyName        = "name"
	keyRequired    = "required"
	keyHeader      = "header"
	keyAPIKey      = "apiKey"
	keyAttributes  = "attributes"
	keyData        = "data"
	keyExample     = "example"
	keyMeta        = "meta"
	keyProperties  = "properties"
	keyString      = "string"
	keyType        = "type"
	typeObject     = "object"
	mediaJSON      = "application/json"

	emptyResponseDesc = "Empty response"
)

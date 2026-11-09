package types

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// FactSchemaVersion is the analytics-fact wire version; a consumer rejects a
// version it does not know.
const FactSchemaVersion = 1

// Fact is one analytics observation of a cube: who (OrgID + Dims), when (TS), how
// much (Measures), and the IdempotencyKey a consumer upserts on so a redelivered
// fact is a no-op. Its JSON is the analytics.fact wire form, byte-identical to the
// Python techai_webutils Fact.to_json (keys sorted, TS in UTC RFC 3339).
type Fact struct {
	// TS is when the observation happened; it is serialized in UTC.
	TS time.Time
	// Dims are the dimension values the fact is grouped by.
	Dims map[string]string
	// Measures are the numeric measures the fact contributes.
	Measures map[string]float64
	// Cube is the cube (fact table) the fact belongs to.
	Cube string
	// OrgID is the organization the fact is partitioned under.
	OrgID string
	// IdempotencyKey is unique per observation; a redelivery overwrites, never
	// double-counts.
	IdempotencyKey string
	// Schema is the wire version (FactSchemaVersion).
	Schema int
}

// factWire is the JSON shape of a decoded Fact.
type factWire struct {
	// Dims are the dimension values.
	Dims map[string]string `json:"dims"`
	// Measures are the numeric measures.
	Measures map[string]float64 `json:"measures"`
	// Cube is the cube name.
	Cube string `json:"cube"`
	// IdempotencyKey is the upsert key.
	IdempotencyKey string `json:"idempotency_key"`
	// OrgID is the partitioning organization.
	OrgID string `json:"org_id"`
	// TS is the RFC 3339 UTC timestamp.
	TS string `json:"ts"`
	// Schema is the wire version.
	Schema int `json:"schema"`
}

// Wire renders the analytics.fact wire form: sorted keys, TS in UTC as RFC 3339 with
// the fractional seconds trimmed. It is the only encoder: plain json.Marshal of a Fact
// does not produce the wire form.
func (f *Fact) Wire() ([]byte, error) {
	dims, measures := f.Dims, f.Measures
	if dims == nil {
		dims = map[string]string{} // Python renders an empty mapping as {}, never null.
	}

	if measures == nil {
		measures = map[string]float64{}
	}

	// A map marshals with its keys sorted (cube, dims, idempotency_key, measures,
	// org_id, schema, ts), the order the Python encoder emits.
	out, err := json.Marshal(map[string]any{
		"cube":            f.Cube,
		"dims":            dims,
		"idempotency_key": f.IdempotencyKey,
		"measures":        measures,
		"org_id":          f.OrgID,
		"schema":          f.Schema,
		"ts":              f.TS.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "fact: encode")
	}

	return out, nil
}

// UnmarshalJSON decodes the analytics.fact wire form; a malformed body or
// timestamp, or a schema other than FactSchemaVersion, is CodeInvalidInput.
func (f *Fact) UnmarshalJSON(b []byte) error {
	var currentFactWire factWire

	err := json.Unmarshal(b, &currentFactWire)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "fact: decode")
	}

	if currentFactWire.Schema != FactSchemaVersion {
		return apperr.New(
			apperr.CodeInvalidInput,
			"fact: unknown schema version "+strconv.Itoa(currentFactWire.Schema),
		)
	}

	instant, err := time.Parse(time.RFC3339Nano, currentFactWire.TS)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "fact: invalid ts")
	}

	*f = Fact{
		TS:             instant,
		Dims:           currentFactWire.Dims,
		Measures:       currentFactWire.Measures,
		Cube:           currentFactWire.Cube,
		OrgID:          currentFactWire.OrgID,
		IdempotencyKey: currentFactWire.IdempotencyKey,
		Schema:         currentFactWire.Schema,
	}

	return nil
}

package chartspec

import (
	"encoding/binary"
	"math"
	"time"

	"github.com/DataDog/sketches-go/ddsketch"
	"github.com/DataDog/sketches-go/ddsketch/store"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// partialCodecVersion is the first byte of an encoded Partial.
const partialCodecVersion byte = 1

// partialHeaderLen is the version byte plus the four float64 fields.
const partialHeaderLen = 1 + 4*8

// EncodePartial serializes p (version byte, sum, count, min, max as big-endian
// float64, then the DDSketch with its index mapping) for storage in a row.
func EncodePartial(p Partial) []byte {
	out := make([]byte, partialHeaderLen, partialHeaderLen+64)
	out[0] = partialCodecVersion
	for i, v := range []float64{p.Sum, p.Count, p.Min, p.Max} {
		binary.BigEndian.PutUint64(out[1+8*i:], math.Float64bits(v))
	}
	if p.Sketch != nil {
		p.Sketch.Encode(&out, false)
	}
	return out
}

// DecodePartial parses an EncodePartial blob; a truncated or unknown-version blob
// is CodeInvalidInput.
func DecodePartial(b []byte) (Partial, error) {
	if len(b) < partialHeaderLen || b[0] != partialCodecVersion {
		return Partial{}, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: malformed partial",
		)
	}
	f := func(i int) float64 {
		return math.Float64frombits(binary.BigEndian.Uint64(b[1+8*i:]))
	}
	p := Partial{Sum: f(0), Count: f(1), Min: f(2), Max: f(3)}
	if rest := b[partialHeaderLen:]; len(rest) > 0 {
		sketch, err := ddsketch.DecodeDDSketch(rest, store.DefaultProvider, nil)
		if err != nil {
			return Partial{}, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"chartspec: malformed partial sketch",
			)
		}
		p.Sketch = sketch
	}
	return p, nil
}

// Truncate returns t in UTC truncated to the start of its grain (minute, hour,
// UTC day or UTC month); an unknown grain returns t in UTC unchanged.
func Truncate(t time.Time, g types.Grain) time.Time {
	t = t.UTC()
	switch g {
	case types.GrainMinute:
		return t.Truncate(time.Minute)
	case types.GrainHour:
		return t.Truncate(time.Hour)
	case types.GrainDay:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	case types.GrainMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return t
	}
}

// Next returns the start of the grain period after the one starting at start; an
// unknown grain returns start unchanged (callers validate with ValidGrain).
func Next(start time.Time, g types.Grain) time.Time {
	switch g {
	case types.GrainMinute:
		return start.Add(time.Minute)
	case types.GrainHour:
		return start.Add(time.Hour)
	case types.GrainDay:
		return start.AddDate(0, 0, 1)
	case types.GrainMonth:
		return start.AddDate(0, 1, 0)
	default:
		return start
	}
}

// ValidGrain reports whether g is one of the grains Truncate and Next know.
func ValidGrain(g types.Grain) bool {
	switch g {
	case types.GrainMinute, types.GrainHour, types.GrainDay, types.GrainMonth:
		return true
	default:
		return false
	}
}

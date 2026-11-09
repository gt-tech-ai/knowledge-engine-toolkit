package chartspec

import (
	"encoding/binary"
	"math"
	"time"

	"github.com/DataDog/sketches-go/ddsketch"
	"github.com/DataDog/sketches-go/ddsketch/store"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// partialMinSlot is the position of Min in an encoded Partial.
const partialMinSlot = 2

// partialMaxSlot is the position of Max in an encoded Partial.
const partialMaxSlot = 3

// partialBodyHint is the capacity reserved for an encoded partial's body.
const partialBodyHint = 64

// partialCodecVersion is the first byte of an encoded Partial.
const partialCodecVersion byte = 1

// partialHeaderLen is the version byte plus the four float64 fields.
const partialHeaderLen = 1 + 4*8

// EncodePartial serializes p (version byte, sum, count, min, max as big-endian
// float64, then the DDSketch with its index mapping) for storage in a row.
func EncodePartial(partial Partial) []byte {
	out := make([]byte, partialHeaderLen, partialHeaderLen+partialBodyHint)

	out[0] = partialCodecVersion

	for i, v := range []float64{partial.Sum, partial.Count, partial.Min, partial.Max} {
		binary.BigEndian.PutUint64(out[1+8*i:], math.Float64bits(v))
	}

	if partial.Sketch != nil {
		partial.Sketch.Encode(&out, false)
	}

	return out
}

// DecodePartial parses an EncodePartial blob; a truncated or unknown-version blob
// is CodeInvalidInput.
func DecodePartial(data []byte) (Partial, error) {
	if len(data) < partialHeaderLen || data[0] != partialCodecVersion {
		return Partial{}, apperr.New(
			apperr.CodeInvalidInput,
			"chartspec: malformed partial",
		)
	}

	callback := func(i int) float64 {
		return math.Float64frombits(binary.BigEndian.Uint64(data[1+8*i:]))
	}
	partial := Partial{
		Sum:   callback(0),
		Count: callback(1),
		Min:   callback(partialMinSlot),
		Max:   callback(partialMaxSlot),
	}

	if rest := data[partialHeaderLen:]; len(rest) > 0 {
		sketch, err := ddsketch.DecodeDDSketch(rest, store.DefaultProvider, nil)
		if err != nil {
			return Partial{}, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"chartspec: malformed partial sketch",
			)
		}

		partial.Sketch = sketch
	}

	return partial, nil
}

// Truncate returns t in UTC truncated to the start of its grain (minute, hour,
// UTC day or UTC month); an unknown grain returns t in UTC unchanged.
func Truncate(timeValue time.Time, g types.Grain) time.Time {
	timeValue = timeValue.UTC()

	switch g {
	case types.GrainMinute:
		return timeValue.Truncate(time.Minute)
	case types.GrainHour:
		return timeValue.Truncate(time.Hour)
	case types.GrainDay:
		return time.Date(
			timeValue.Year(),
			timeValue.Month(),
			timeValue.Day(),
			0,
			0,
			0,
			0,
			time.UTC,
		)
	case types.GrainMonth:
		return time.Date(timeValue.Year(), timeValue.Month(), 1, 0, 0, 0, 0, time.UTC)
	default:
		return timeValue
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

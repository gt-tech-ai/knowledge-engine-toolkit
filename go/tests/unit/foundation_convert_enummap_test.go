package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	apprpc "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/convert"
)

type enumP int

type enumD string

const (
	pUnspecified enumP = 0
	pOne         enumP = 1
	pTwo         enumP = 2

	dOne enumD = "one"
	dTwo enumD = "two"
)

func newTestMapper() apprpc.EnumMapper[enumP, enumD] {
	return apprpc.NewEnumMapper("test enum", pUnspecified, enumD(""),
		map[enumP]enumD{pOne: dOne, pTwo: dTwo})
}

// TestEnumMapper tests the bidirectional proto↔domain enum mapper's three conversion
// shapes.
//
// Why this test is important:
//   - It is the single source of truth for a value's proto↔domain correspondence across
//     the RPC converters; a wrong reverse mapping, a missing "no filter" sentinel, or a
//     swallowed unknown value would silently mis-convert an enum at the transport edge.
//
// What it tests:
//   - ToProtoValue / ToDomainOK round-trip a known value; an unmapped domain value falls
//     back to protoZero.
//   - ToDomainOrError rejects an unknown proto value as CodeInvalidInput.
//   - ToDomainFilter treats protoZero (unspecified) as "no filter" (ok=false) and maps a
//     known value.
//   - ToDomainEntries exposes the forward table for derived projections.
func TestEnumMapper(t *testing.T) {
	t.Parallel()

	enumMapper := newTestMapper()

	assert.Equal(t, pOne, enumMapper.ToProtoValue(dOne))

	d, found := enumMapper.ToDomainOK(pTwo)
	assert.True(t, found)
	assert.Equal(t, dTwo, d)

	assert.Equal(
		t,
		pUnspecified,
		enumMapper.ToProtoValue(enumD(textAbsent)),
	) // unmapped → protoZero fallback

	_, err := enumMapper.ToDomainOrError(enumP(enumPArg99))
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, found = enumMapper.ToDomainFilter(pUnspecified) // unspecified → "no filter"
	assert.False(t, found)

	fd, found := enumMapper.ToDomainFilter(pOne)
	assert.True(t, found)
	assert.Equal(t, dOne, fd)

	assert.Len(t, enumMapper.ToDomainEntries(), wantToDomainEntriesLen2)
}

// TestNewEnumMapper_ReverseCollisionPanics tests the loud-at-init guard against a
// non-invertible table.
//
// Why this test is important:
//   - The reverse (domain→proto) table requires unique domain values; a collision would
//     make ToProtoValue a silent last-writer-wins under Go's randomized map iteration.
//     The mappers are built at package init, so the contract is a loud panic at startup,
//     never a silent mis-mapping in prod.
//
// What it tests:
//   - Two proto values mapping to the same domain value panics at construction.
func TestNewEnumMapper_ReverseCollisionPanics(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		apprpc.NewEnumMapper("dup", pUnspecified, enumD(""),
			map[enumP]enumD{pOne: dOne, pTwo: dOne}) // both → dOne
	})
}

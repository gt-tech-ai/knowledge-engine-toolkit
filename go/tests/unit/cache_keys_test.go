package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cache"
)

// TestAccessFingerprint_RotatesOnScopeChange tests that the access fingerprint is stable
// for the same scope, order-insensitive (a set), and rotates when any scope value
// changes.
//
// Why this test is important:
//   - access_fp confines a cached result to the exact access boundary it was computed
//     under. If two materially-different scopes hashed to the SAME fingerprint, one
//     caller could be served another's access-scoped result — a cross-tenant or
//     cross-access-level leak. If a changed scope did NOT rotate the fingerprint, a
//     grant/revoke would keep serving now-wrong cached entries. This is the property that
//     makes authz-change invalidation "by key rotation."
//
// What it tests:
//   - same scope → same fp; the same members in a different order → same fp (set
//     semantics); a changed access level and a smaller access set each → a different fp.
func TestAccessFingerprint_RotatesOnScopeChange(t *testing.T) {
	t.Parallel()

	base := cache.AccessFingerprint(textScopeA, textScopeB, textRestricted)
	assert.Equal(
		t,
		base,
		cache.AccessFingerprint(textScopeA, textScopeB, textRestricted),
		"stable for the same scope",
	)
	assert.Equal(
		t,
		base,
		cache.AccessFingerprint(textScopeB, textScopeA, textRestricted),
		"order-insensitive (a set)",
	)
	assert.NotEqual(
		t,
		base,
		cache.AccessFingerprint(textScopeA, textScopeB, "public"),
		"a changed access level rotates the fp",
	)
	assert.NotEqual(
		t,
		base,
		cache.AccessFingerprint(textScopeA, textRestricted),
		"a smaller access set rotates the fp",
	)
	assert.NotEmpty(t, base)
}

// TestFilterHash_StableAndDiscriminating tests that the filter hash is stable for one
// filter and differs for a different filter, so the count cache reuses a total across
// page flips for a given filter but never reuses it for a different one.
//
// Why this test is important:
//   - The filter_hash is the count key component that ties a cached total to the exact
//     filtered set it counted. A hash that collided two different filters would serve a
//     wrong total; one that was unstable for the same filter would defeat the "counted
//     once, reused across flips" goal.
//
// What it tests:
//   - same filter → same hash; a different filter value → a different hash; a nil
//     (match-all) filter hashes to a stable non-empty value. (Reordered-but-equivalent
//     composite filters DO hash differently — the encoding is order-preserving — a
//     hit-rate cost, not a correctness bug.)
func TestFilterHash_StableAndDiscriminating(t *testing.T) {
	t.Parallel()

	filterClause := types.FilterClause{
		Field:    textStatus,
		Operator: types.OpEq,
		Value:    textIndexed,
	}
	currentFilterClause := types.FilterClause{
		Field:    textStatus,
		Operator: types.OpEq,
		Value:    textFailed,
	}
	f1Again := types.FilterClause{
		Field:    textStatus,
		Operator: types.OpEq,
		Value:    textIndexed,
	}
	assert.Equal(
		t,
		cache.FilterHash(filterClause),
		cache.FilterHash(f1Again),
		"stable for the same filter",
	)
	assert.NotEqual(
		t,
		cache.FilterHash(filterClause),
		cache.FilterHash(currentFilterClause),
		"a different filter → a different hash",
	)
	assert.NotEmpty(t, cache.FilterHash(nil), "a nil (match-all) filter hashes stably")
}

// TestCountKey_GenerationRotationChangesKey tests that rotating the generation token
// changes the count key — the mechanism by which a coarse bust orphans every prior entry
// for a (tenant, resource) across all access_fp/filter_hash at once, and that a different
// access_fp is a different key (no cross-access reuse).
//
// Why this test is important:
//   - The count cache key fragments by access_fp + filter_hash, so a single-key delete
//     could never coarse-bust everyone. The generation component is what makes one write
//     (a token rotate) bust the whole bucket. If a new generation did NOT change the key,
//     the bust would be a silent no-op; if a different access_fp shared a key, a count
//     would leak across an access boundary.
//
// What it tests:
//   - a new generation token → a different key; stable inputs → a stable key; a different
//     access_fp → a different key.
func TestCountKey_GenerationRotationChangesKey(t *testing.T) {
	t.Parallel()

	text := cache.AccessFingerprint(textOrg1)
	otherText := cache.FilterHash(nil)
	thirdText := cache.CountKey(textTenant1, textConnectorInfo, textGenA, text, otherText)
	assert.Equal(
		t,
		thirdText,
		cache.CountKey(textTenant1, textConnectorInfo, textGenA, text, otherText),
		"stable inputs → stable key",
	)
	assert.NotEqual(
		t,
		thirdText,
		cache.CountKey(textTenant1, textConnectorInfo, "gen-b", text, otherText),
		"a new generation orphans the old key",
	)
	assert.NotEqual(
		t,
		thirdText,
		cache.CountKey(
			textTenant1,
			textConnectorInfo,
			textGenA,
			cache.AccessFingerprint("org-2"),
			otherText,
		),
		"a different access_fp is a different key",
	)
	assert.NotEqual(
		t,
		cache.CountGenKey(textTenant1, textConnectorInfo),
		cache.CountGenKey(textTenant1, textDocument),
		"the generation key is per (tenant, resource)",
	)
}

// TestSuggestKey_DiscriminatesFieldAccessPrefix tests the suggest key varies by field,
// access_fp, and prefix, so a suggestion is never served across those boundaries, and
// that a user-controlled prefix containing the key delimiter cannot collide with another
// key.
//
// Why this test is important:
//   - The prefix is raw typeahead input; folded into the key it must not let a crafted
//     prefix (e.g. one containing ':') collide with a different (field, access) key and
//     serve the wrong suggestions.
//
// What it tests:
//   - same inputs → same key; field/prefix/access/limit each vary the key; a
//     delimiter-bearing prefix produces a distinct key rather than colliding.
func TestSuggestKey_DiscriminatesFieldAccessPrefix(t *testing.T) {
	t.Parallel()

	text := cache.AccessFingerprint(textScopeA, textRestricted)
	base := cache.SuggestKey(
		textTenant1,
		textDocument,
		textStatus,
		text,
		textInd,
		suggestKeyInput,
	)
	assert.Equal(
		t,
		base,
		cache.SuggestKey(
			textTenant1,
			textDocument,
			textStatus,
			text,
			textInd,
			suggestKeyInput,
		),
	)
	assert.NotEqual(
		t,
		base,
		cache.SuggestKey(
			textTenant1,
			textDocument,
			textTitle,
			text,
			textInd,
			suggestKeyInput,
		),
		"field varies the key",
	)
	assert.NotEqual(
		t,
		base,
		cache.SuggestKey(
			textTenant1,
			textDocument,
			textStatus,
			text,
			"fai",
			suggestKeyInput,
		),
		"prefix varies the key",
	)

	testSuggestKeyDiscriminatesFieldAccessPrefixCases(t, text, base)
}

// testSuggestKeyDiscriminatesFieldAccessPrefixCases continues
// TestSuggestKey_DiscriminatesFieldAccessPrefix.
func testSuggestKeyDiscriminatesFieldAccessPrefixCases(t *testing.T, text, base string) {
	t.Helper()

	assert.NotEqual(t, base, cache.SuggestKey(
		textTenant1,
		textDocument,
		textStatus,
		cache.AccessFingerprint(
			textScopeB,
			textRestricted,
		),
		textInd,
		suggestKeyInput,
	), "access varies the key")
	assert.NotEqual(
		t,
		base,
		cache.SuggestKey(
			textTenant1,
			textDocument,
			textStatus,
			text,
			textInd,
			suggestKeyInputPrefix,
		),
		"limit varies the key (a limit-bounded page must not be shared across limits)",
	)
	assert.NotEqual(
		t,
		cache.SuggestKey(
			textTenant1,
			textDocument,
			textStatus,
			text,
			"a:b",
			suggestKeyInput,
		),
		cache.SuggestKey(
			textTenant1,
			textDocument,
			"status:a",
			text,
			textB,
			suggestKeyInput,
		),
		"a delimiter-bearing prefix cannot collide with a different field/prefix split",
	)
}

const (
	// suggestKeyInput is the suggest key input the tests use.
	suggestKeyInput = 10

	// suggestKeyInputPrefix is the suggest key input prefix the tests use.
	suggestKeyInputPrefix = 25
)

const (
	// textGenA is the literal "gen-a".
	textGenA = "gen-a"

	// textInd is the literal "ind".
	textInd = "ind"

	// textScopeB is the literal "scope-b".
	textScopeB = "scope-b"

	// textConnectorInfo is the literal "connector_info".
	textConnectorInfo = "connector_info"

	// textScopeA is the literal "scope-a".
	textScopeA = "scope-a"

	// textRestricted is the literal "restricted".
	textRestricted = "restricted"
)

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
	filterClauseValue := types.FilterClause{
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
		cache.FilterHash(filterClauseValue),
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
	text2 := cache.FilterHash(nil)
	text3 := cache.CountKey(textTenant1, textConnectorInfo, textGenA, text, text2)
	assert.Equal(
		t,
		text3,
		cache.CountKey(textTenant1, textConnectorInfo, textGenA, text, text2),
		"stable inputs → stable key",
	)
	assert.NotEqual(
		t,
		text3,
		cache.CountKey(textTenant1, textConnectorInfo, "gen-b", text, text2),
		"a new generation orphans the old key",
	)
	assert.NotEqual(
		t,
		text3,
		cache.CountKey(
			textTenant1,
			textConnectorInfo,
			textGenA,
			cache.AccessFingerprint("org-2"),
			text2,
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
		limit10,
	)
	assert.Equal(
		t,
		base,
		cache.SuggestKey(textTenant1, textDocument, textStatus, text, textInd, limit10),
	)
	assert.NotEqual(
		t,
		base,
		cache.SuggestKey(textTenant1, textDocument, textTitle, text, textInd, limit10),
		"field varies the key",
	)
	assert.NotEqual(
		t,
		base,
		cache.SuggestKey(textTenant1, textDocument, textStatus, text, "fai", limit10),
		"prefix varies the key",
	)
	assert.NotEqual(t, base, cache.SuggestKey(
		textTenant1,
		textDocument,
		textStatus,
		cache.AccessFingerprint(
			textScopeB,
			textRestricted,
		),
		textInd,
		limit10,
	), "access varies the key")
	assert.NotEqual(
		t,
		base,
		cache.SuggestKey(textTenant1, textDocument, textStatus, text, textInd, limit25),
		"limit varies the key (a limit-bounded page must not be shared across limits)",
	)
	assert.NotEqual(t,
		cache.SuggestKey(textTenant1, textDocument, textStatus, text, "a:b", limit10),
		cache.SuggestKey(textTenant1, textDocument, "status:a", text, textB, limit10),
		"a delimiter-bearing prefix cannot collide with a different field/prefix split")
}

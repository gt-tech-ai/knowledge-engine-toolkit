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
	base := cache.AccessFingerprint("scope-a", "scope-b", "restricted")
	assert.Equal(
		t,
		base,
		cache.AccessFingerprint("scope-a", "scope-b", "restricted"),
		"stable for the same scope",
	)
	assert.Equal(
		t,
		base,
		cache.AccessFingerprint("scope-b", "scope-a", "restricted"),
		"order-insensitive (a set)",
	)
	assert.NotEqual(
		t,
		base,
		cache.AccessFingerprint("scope-a", "scope-b", "public"),
		"a changed access level rotates the fp",
	)
	assert.NotEqual(
		t,
		base,
		cache.AccessFingerprint("scope-a", "restricted"),
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
	f1 := types.FilterClause{Field: "status", Operator: types.OpEq, Value: "indexed"}
	f2 := types.FilterClause{Field: "status", Operator: types.OpEq, Value: "failed"}
	f1Again := types.FilterClause{Field: "status", Operator: types.OpEq, Value: "indexed"}
	assert.Equal(
		t,
		cache.FilterHash(f1),
		cache.FilterHash(f1Again),
		"stable for the same filter",
	)
	assert.NotEqual(
		t,
		cache.FilterHash(f1),
		cache.FilterHash(f2),
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
	fp := cache.AccessFingerprint("org-1")
	fh := cache.FilterHash(nil)
	k1 := cache.CountKey("tenant-1", "connector_info", "gen-a", fp, fh)
	assert.Equal(
		t,
		k1,
		cache.CountKey("tenant-1", "connector_info", "gen-a", fp, fh),
		"stable inputs → stable key",
	)
	assert.NotEqual(
		t,
		k1,
		cache.CountKey("tenant-1", "connector_info", "gen-b", fp, fh),
		"a new generation orphans the old key",
	)
	assert.NotEqual(
		t,
		k1,
		cache.CountKey(
			"tenant-1",
			"connector_info",
			"gen-a",
			cache.AccessFingerprint("org-2"),
			fh,
		),
		"a different access_fp is a different key",
	)
	assert.NotEqual(
		t,
		cache.CountGenKey("tenant-1", "connector_info"),
		cache.CountGenKey("tenant-1", "document"),
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
	fp := cache.AccessFingerprint("scope-a", "restricted")
	base := cache.SuggestKey("tenant-1", "document", "status", fp, "ind", 10)
	assert.Equal(
		t,
		base,
		cache.SuggestKey("tenant-1", "document", "status", fp, "ind", 10),
	)
	assert.NotEqual(
		t,
		base,
		cache.SuggestKey("tenant-1", "document", "title", fp, "ind", 10),
		"field varies the key",
	)
	assert.NotEqual(
		t,
		base,
		cache.SuggestKey("tenant-1", "document", "status", fp, "fai", 10),
		"prefix varies the key",
	)
	assert.NotEqual(t, base, cache.SuggestKey(
		"tenant-1",
		"document",
		"status",
		cache.AccessFingerprint(
			"scope-b",
			"restricted",
		),
		"ind",
		10,
	), "access varies the key")
	assert.NotEqual(
		t,
		base,
		cache.SuggestKey("tenant-1", "document", "status", fp, "ind", 25),
		"limit varies the key (a limit-bounded page must not be shared across limits)",
	)
	assert.NotEqual(t,
		cache.SuggestKey("tenant-1", "document", "status", fp, "a:b", 10),
		cache.SuggestKey("tenant-1", "document", "status:a", fp, "b", 10),
		"a delimiter-bearing prefix cannot collide with a different field/prefix split")
}

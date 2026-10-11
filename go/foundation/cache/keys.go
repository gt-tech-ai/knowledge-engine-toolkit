package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// keySeparator joins key parts. NUL cannot appear in an identifier, uuid, or access-level
// string, so distinct part tuples can never run together to forge the same joined string
// (a collision).
const keySeparator = "\x00"

// hashParts returns a hex sha256 over the parts joined by the NUL separator — a
// fixed-length, delimiter-safe digest used to fold variable (and possibly
// user-controlled) key components so no crafted part can inject a delimiter and collide
// with a different key.
func hashParts(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, keySeparator)))
	return hex.EncodeToString(sum[:])
}

// AccessFingerprint hashes an access SCOPE — the exact resolved-context values the
// query's authz predicate applies (e.g. the set of accessible group ids plus an access
// level, or a tenant id) — into a compact fingerprint. It is order-insensitive (the scope
// is a set: the parts are sorted first), so the same access hashes equally regardless of
// argument order, and ANY change to the scope rotates the fingerprint — which is how an
// authz change invalidates cached entries (by key rotation, never reach-in). The caller
// MUST pass the same values the WHERE clause applies (never raw client input), so the key
// can never be spoofed narrower than the real access boundary.
func AccessFingerprint(scope ...string) string {
	sorted := append([]string(nil), scope...)
	sort.Strings(sorted)
	return hashParts(sorted...)
}

// FilterHash hashes a list-query filter into a stable key component, reusing the
// query-fingerprint (filter-only — no sort). Two structurally-identical filters hash
// equally (so a total is reused across page flips for one filter); a different filter
// hashes differently. The encoding is order-preserving, so two SEMANTICALLY-equal but
// differently-ordered composite filters hash differently — a cache-hit-rate cost, never a
// correctness bug (a distinct key is still scoped correctly).
func FilterHash(filter types.Filter) string {
	return hex.EncodeToString(types.Fingerprint(nil, filter))
}

// SuggestKey builds the value-suggestion cache key: the "suggest:" namespace then a
// digest of (tenant, resource, field, access_fp, prefix, limit). The limit is part of the
// key because the cached page is limit-bounded — two callers with the same prefix but
// different limits must NOT share an entry (a smaller-limit caller would otherwise read a
// larger-limit page, or vice-versa). The user-controlled prefix is folded into the
// digest, so it can never inject a delimiter to collide with another key.
func SuggestKey(tenant, resource, field, accessFP, prefix string, limit int) string {
	return "suggest:" + hashParts(
		tenant,
		resource,
		field,
		accessFP,
		prefix,
		strconv.Itoa(limit),
	)
}

// CountKey builds the total_count cache key: the "count:" namespace then a digest of
// (tenant, resource, generation, access_fp, filter_hash). The generation component is
// what makes a coarse bust O(1): rotating the generation (see CountGenKey) changes every
// count key for the (tenant, resource) at once, orphaning all prior entries across every
// access_fp/filter_hash.
func CountKey(tenant, resource, generation, accessFP, filterHash string) string {
	return "count:" + hashParts(tenant, resource, generation, accessFP, filterHash)
}

// CountGenKey is the key holding the current generation token for a (tenant, resource)'s
// count cache. A resource write rotates the token stored here (one Set), coarse-busting
// the whole bucket in a single O(1) operation. tenant and resource are identifiers with
// no ':' so the readable form is unambiguous.
func CountGenKey(tenant, resource string) string {
	return "count:gen:" + tenant + ":" + resource
}

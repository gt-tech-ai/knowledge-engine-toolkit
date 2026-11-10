package types

// KeysetCursor carries the previous page's last-row ordering values for a keyset seek
// The seek predicate pairs these with the resource's ordered keyset columns
// (the caller's sort fields, then the resource tiebreaker column, then id).
//
// A cursor is bound to the ACCESS SCOPE it was minted in (the tenant and access
// restrictions the store's base query applies). The Fingerprint binds only (sort ⊕
// filter), NOT that scope — the scope lives in the store's base query, outside the user
// filter — so a cursor replayed against a different scope (or after the caller's access
// changed) is not rejected; it just seeks from a boundary row that may not belong to the
// new scope, which can dup/skip rows within that scope (never a cross-scope leak: the
// base query still constrains the results). Callers MUST mint a fresh cursor when the
// access scope changes rather than carrying one across scopes.
type KeysetCursor struct {
	// SortValues are the last row's values for each NON-id keyset column, in ORDER BY order
	// (the caller's sort fields then the resource's tiebreaker column, e.g. created_at).
	SortValues []any
	// ID is the last row's id — the always-present final tiebreaker column's value.
	ID string
	// Fingerprint binds the cursor to the (sort ⊕ filter) it was minted under; a mismatch
	// against the current request is rejected so a cursor can't seek an incompatible
	// order. It does NOT bind the access scope (see the type doc) — cursors are
	// scope-bound by contract.
	Fingerprint []byte
}

package listquery

import (
	"sort"
	"strconv"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// SuggestFallbackLimit bounds a suggest page when the caller passes a non-positive limit
// — the store's belt-and-suspenders after the transport edge normally clamps to the
// suggest maximum.
const SuggestFallbackLimit = 10

// SuggestPageNumber parses a suggest result cursor — a bare 1-based page number (the same
// convention as the offset list pager). An empty, non-numeric, or non-positive token
// safely restarts at page 1, so a malformed cursor can never produce a negative OFFSET.
func SuggestPageNumber(token string) int {
	if token == "" {
		return 1
	}
	if n, err := strconv.Atoi(token); err == nil && n > 1 {
		return n
	}
	return 1
}

// SuggestPage builds a single-column suggest result page from values a store fetched with
// a has-more probe (limit+1 rows, already DISTINCT + prefix-matched + ascending in SQL).
// It slices to limit, mints the next 1-based cursor only when the probe row was present,
// and stamps the page size + number. A non-positive limit falls back to
// SuggestFallbackLimit.
func SuggestPage(values []string, limit, pageNum int) *types.Page[string] {
	if limit <= 0 {
		limit = SuggestFallbackLimit
	}
	next := ""
	if len(values) > limit {
		values = values[:limit]
		next = strconv.Itoa(pageNum + 1)
	}
	return &types.Page[string]{
		Items:      values,
		NextCursor: next,
		PageSize:   limit,
		PageNumber: pageNum,
	}
}

// MergeDistinct builds a suggest page for a field spanning MORE THAN ONE column — a
// member display_name over first_name + last_name (UNION ALL + GLOBAL dedup). Each input
// slice is one column's distinct prefix matches (each already capped at offset+limit+1 by
// its query, which is sufficient to fill the page). It merges them into one GLOBALLY
// distinct, ascending set BEFORE applying offset+limit, so a value present in more than
// one column appears exactly once with no page-boundary dup/skip. The windowed slice
// (limit+1 for the has-more probe) is handed to SuggestPage.
func MergeDistinct(columns [][]string, limit, pageNum int) *types.Page[string] {
	if limit <= 0 {
		limit = SuggestFallbackLimit
	}
	seen := make(map[string]struct{})
	merged := make([]string, 0)
	for _, col := range columns {
		for _, v := range col {
			if _, dup := seen[v]; dup {
				continue
			}
			seen[v] = struct{}{}
			merged = append(merged, v)
		}
	}
	sort.Strings(merged)

	offset := (pageNum - 1) * limit
	if offset >= len(merged) {
		return SuggestPage(nil, limit, pageNum)
	}
	end := offset + limit + 1 // +1 = the has-more probe row SuggestPage detects
	if end > len(merged) {
		end = len(merged)
	}
	return SuggestPage(merged[offset:end], limit, pageNum)
}

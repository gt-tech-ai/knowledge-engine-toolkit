package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// TestSuggestPageNumber tests the 1-based suggest cursor parser.
//
// Why this test is important:
//   - The suggest cursor is a bare 1-based page number; a malformed or empty token must
//     restart at page
//
// 1 (never a negative offset or a panic), matching the store convention. A parser that
//
//	let a bad token through would compute a negative OFFSET and break the query.
//
// What it tests:
//   - "" and non-numeric / non-positive tokens → 1; a valid n > 1 → n.
func TestSuggestPageNumber(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, listquery.SuggestPageNumber(""))
	assert.Equal(t, 1, listquery.SuggestPageNumber("abc"))
	assert.Equal(t, 1, listquery.SuggestPageNumber(text0))
	assert.Equal(t, 1, listquery.SuggestPageNumber("-4"))
	assert.Equal(t, 1, listquery.SuggestPageNumber("1"))
	assert.Equal(t, wantSuggestPageNumber, listquery.SuggestPageNumber("3"))
}

// TestSuggestPage tests the single-column suggest page builder (SQL already did
// DISTINCT+ORDER+LIMIT+1).
//
// Why this test is important:
//   - The has-more probe (fetch limit+1) + the next-cursor mint is the paging contract
//     shared by every suggest store; an off-by-one here would drop the last value of a
//     page or loop forever on a stale cursor. Centralizing it keeps every resource's
//     suggest paging identical.
//
// What it tests:
//   - A probe row present (len == limit+1) → sliced to limit + next cursor = pageNum+1;
//     no probe row → values unchanged + empty cursor.
func TestSuggestPage(t *testing.T) {
	t.Parallel()

	// Probe row present → has more.
	got := listquery.SuggestPage([]string{textA, textB, textC}, suggestPageInput, 1)
	assert.Equal(t, []string{textA, textB}, got.Items)
	assert.Equal(t, "2", got.NextCursor)
	assert.Equal(t, wantPageSizePage, got.PageSize)
	assert.Equal(t, 1, got.PageNumber)

	// No probe row → last page.
	last := listquery.SuggestPage(
		[]string{textA, textB},
		suggestPageInput,
		suggestPageInputPage,
	)
	assert.Equal(t, []string{textA, textB}, last.Items)
	assert.Empty(t, last.NextCursor)
	assert.Equal(t, wantPageNumber, last.PageNumber)
}

// TestMergeDistinct tests the multi-column global-dedup + paging for a suggest field that
// spans >1 column (a member display_name over first_name + last_name, UNION ALL + global
// dedup).
//
// Why this test is important:
//   - The failure mode this guards is a value that matches in BOTH columns being
//     duplicated across a page boundary, or a per-branch dedup silently dropping a value.
//     Global dedup on the resolved value BEFORE the offset/limit is the only correct
//     shape; this pins it.
//
// What it tests:
//   - A value present in BOTH columns appears exactly once (no duplicate).
//   - Disjoint columns merge into one ascending set, paged with no dup/skip across the
//     boundary.
func TestMergeDistinct(t *testing.T) {
	t.Parallel()

	// Both branches match the same value → exactly once.
	both := listquery.MergeDistinct(
		[][]string{{textSmithTitle}, {textSmithTitle}},
		mergeDistinctInput,
		1,
	)
	assert.Equal(t, []string{textSmithTitle}, both.Items)
	assert.Empty(t, both.NextCursor)

	// Disjoint columns, page 1 of 2 (limit 2) → the two smallest, has-more.
	cols := [][]string{{"Bob", "Dave"}, {textAlice, "Carol"}}
	p1 := listquery.MergeDistinct(cols, mergeDistinctInputDistinct, 1)
	assert.Equal(t, []string{textAlice, "Bob"}, p1.Items)
	assert.Equal(t, "2", p1.NextCursor)

	// Page 2 → the next two, no dup/skip across the boundary, no further pages.
	secondP := listquery.MergeDistinct(
		cols,
		mergeDistinctInputDistinct,
		mergeDistinctInputDistinct,
	)
	assert.Equal(t, []string{"Carol", "Dave"}, secondP.Items)
	assert.Empty(t, secondP.NextCursor)
}

const (
	// mergeDistinctInput is the limit passed to MergeDistinct in TestMergeDistinct.
	mergeDistinctInput = 10

	// mergeDistinctInputDistinct is the limit passed to MergeDistinct in
	// TestMergeDistinct, and the pageNum passed to MergeDistinct in TestMergeDistinct.
	mergeDistinctInputDistinct = 2

	// suggestPageInput is the limit passed to SuggestPage in TestSuggestPage.
	suggestPageInput = 2

	// suggestPageInputPage is the pageNum passed to SuggestPage in TestSuggestPage.
	suggestPageInputPage = 3

	// wantPageNumber is the expected page number.
	wantPageNumber = 3

	// wantPageSizePage is the expected page size page.
	wantPageSizePage = 2

	// wantSuggestPageNumber is the expected suggest page number.
	wantSuggestPageNumber = 3
)

const (
	// textSmithTitle is the literal "Smith".
	textSmithTitle = "Smith"
)

package listquery

import (
	"bytes"
	"context"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// DefaultCountCap is the fallback total_count cap when a reader leaves Bounds.CountCap
// unset, so the offset pager's count is ALWAYS bounded — never a full-table COUNT on a
// huge set. It mirrors the stores.count_cap config default; a store that wires config
// overrides it via Bounds.CountCap.
const DefaultCountCap = 10000

// Bounds is the page-size policy for a read: DefaultSize when the client omits a size,
// MaxSize (when > 0) clamps an oversized client size so an unbounded LIMIT can't reach
// the query, and CountCap bounds the offset arm's total_count.
type Bounds struct {
	// DefaultSize is the page size used when the request omits one (a final floor of 10
	// applies).
	DefaultSize int
	// MaxSize, when > 0, is the ceiling a client-supplied page size is clamped to.
	MaxSize int
	// CountCap bounds the offset arm's total_count: rows are counted only up to the cap,
	// so a per-page count never pays a full-table COUNT. ≤ 0 falls back to
	// DefaultCountCap, so a count is always bounded. A tally over the cap is reported as
	// the cap with an estimate flag.
	CountCap int
}

// countCap resolves the effective total_count cap for the offset arm: the reader's
// Bounds.CountCap when set, else DefaultCountCap — so the count is bounded even for a
// store that never wired config.
func (b Bounds) countCap() int {
	if b.CountCap > 0 {
		return b.CountCap
	}
	return DefaultCountCap
}

// Keyset carries the per-resource data Run needs to serve the keyset (seek) arm and to
// mint the forward keyset cursor. A store opts into keyset pagination by setting
// Reader.Keyset; a store that has not (offset-only) leaves it nil. The order +
// fingerprint inputs (Resolve, Sort, Tiebreaker, Filter) live on the Reader, shared with
// the offset arm.
type Keyset[T any] struct {
	// RowValue returns a row's value for a resolved keyset column (the id column as its
	// string form), and false when the column is not one this store can extract — a guard
	// against a silently corrupt cursor if a sortable column is added without updating
	// the extractor. A timestamp column is returned as its time.Time; Run encodes it into
	// the token.
	RowValue func(row T, column string) (any, bool)

	// ParseID converts the token's id string to the value the seek binds against the id
	// column (e.g. uuid.Parse). Nil keeps the id a string. Kept here so foundation stays
	// uuid-free while the seek binds the id's native type.
	ParseID func(id string) (any, error)

	// TimestampCols names the keyset columns carried as time.Time, so the token encodes
	// them as a unix-microsecond int64 and restores them before the seek. Sourced from
	// the generated schema (ResourceSchema.TimeColumns), never hand-listed.
	TimestampCols map[string]bool
}

// Reader is the configuration for one paginated read. The store fills it and hands it to
// Run: the datastore ListRunner (its scoped base query + typed execution), the domain
// mapper, the resolver + sort + tiebreaker that define the order, the user filter, an
// optional keyset spec, and whether the offset arm computes total_count. Everything else
// — decode, seek, envelope, token — is Run's.
type Reader[T, D any] struct {
	// Runner is the datastore ListRunner: the store's scoped base query + typed execution.
	Runner interfaces.ListRunner[T]
	// Filter is the user's (unresolved) filter; Run resolves its columns via Resolve
	// before use.
	Filter types.Filter
	// ToDomain maps one datastore row to its domain entity.
	ToDomain func(T) (D, error)
	// Resolve maps a field name to its resolved storage column (the allow-list gate).
	Resolve ColumnFn
	// ResolveJoin maps a field name to its joined-table target, or nil for a same-table
	// field. Optional: a resource with no joined filter fields leaves it nil (no joins).
	// Build it from the resource's allow-list with (*Map).Join.
	ResolveJoin JoinFn
	// ResolveOrdinal maps a field name to its value-ordinal sort order, or nil for a
	// field with no ordinal. Optional: a resource with no ordinal-sorted fields leaves it
	// nil. Build it from the resource's allow-list with (*Map).Ordinal.
	ResolveOrdinal OrdinalFn
	// ResolveDerived maps a sort field name to a COMPUTED / CORRELATED sort spec — a
	// correlated aggregate (a Documents/Members count) or a timestamp-plus-interval (a
	// connector's Next Sync) — or nil for a plain/joined/ordinal field. Optional: a
	// resource with no derived sort fields leaves it nil. Supplied by the STORE (a
	// derived sort's correlation is repos-tier knowledge, not a proto-declared column),
	// unlike ResolveJoin/ResolveOrdinal which come from the generated allow-list.
	ResolveDerived DerivedFn
	// MapErr maps a datastore error to a domain error (nil ⇒ pass through unchanged).
	MapErr func(error) error
	// Keyset is the optional keyset spec; nil means this reader is offset-only (a keyset
	// token is rejected).
	Keyset *Keyset[T]
	// Tiebreaker is the resource's final ordering column (after Sort), before the id column.
	Tiebreaker string
	// Sort is the caller's ordering; empty ⇒ the default order (Tiebreaker, id), the only
	// keyset-eligible order.
	Sort []types.OrderField
	// Bounds carries the page-size limits and the total_count cap.
	Bounds Bounds
	// Count requests total_count on the offset arm (bounded by Bounds.CountCap; ignored
	// under skip-count).
	Count bool
}

// Run executes a paginated read and DISPATCHES on the decoded page token: an offset arm
// (OFFSET (N-1)*limit, + optional total_count) or a keyset arm (the forward seek). It
// resolves the user filter and the keyset-column order ONCE, up front, so everything
// handed to the ListRunner is resolved-column data; the runner never touches the
// allow-list. A keyset token against an offset-only reader (Keyset == nil) is rejected as
// unsupported.
func Run[T, D any](
	ctx context.Context,
	page types.PageRequest,
	r Reader[T, D],
) (*types.Page[D], error) {
	tok, err := DecodeToken(page.Cursor)
	if err != nil {
		return nil, err
	}
	cols := KeysetColumns(
		r.Resolve,
		r.ResolveJoin,
		r.ResolveOrdinal,
		r.ResolveDerived,
		r.Sort,
		r.Tiebreaker,
	)
	order := orderFields(cols)
	userF := ResolveFilter(r.Resolve, r.ResolveJoin, r.Filter)
	limit := pageLimit(page, r.Bounds)
	if tok.Keyset != nil {
		if r.Keyset == nil {
			return nil, apperr.InvalidInput(
				"keyset pagination is not supported for this resource",
			)
		}
		if !defaultOrdering(r) {
			return nil, apperr.InvalidInput(
				"keyset (infinite-scroll) pagination is only available for the default ordering; " +
					"a custom sort uses page navigation",
			)
		}
		return keysetPage(ctx, *tok.Keyset, cols, order, userF, limit, r)
	}
	return offsetPage(ctx, int(tok.OffsetPage), cols, order, userF, limit, r)
}

// defaultOrdering reports whether a keyset-wired read is on its DEFAULT ordering — the
// only ordering keyset (infinite scroll) is allowed on (curated set = default only).
// Keyset is served by the resource's covering composite index `(scope…, keyset_col, id)`;
// a custom sort has no covering index, so it would ship a latent full-scan + filesort.
// The default ordering is "no explicit sort" (the client omits sort → server default
// `(keyset_col, id)`); an explicit sort routes to the offset pager instead. Broadening
// the curated set (covering indexes for named sorts) is a future extension.
func defaultOrdering[T, D any](r Reader[T, D]) bool {
	return r.Keyset != nil && len(r.Sort) == 0
}

// pageLimit resolves the effective LIMIT: the client's page size, defaulted to the
// reader's default (10 as a final floor) and clamped to the configured maximum.
func pageLimit(page types.PageRequest, b Bounds) int {
	limit := page.PageSize
	if limit <= 0 {
		limit = b.DefaultSize
		if limit <= 0 {
			limit = 10
		}
	}
	if b.MaxSize > 0 && limit > b.MaxSize {
		limit = b.MaxSize
	}
	return limit
}

// offsetPage serves the offset (numeric-pager) arm: OFFSET (pageNum-1)*limit, fetching
// limit+1 to detect has-more and, when Count is set, the total. Its next token is a
// forward keyset cursor for a keyset-wired reader (so infinite scroll can bridge off any
// offset page) or the next offset page.
func offsetPage[T, D any](
	ctx context.Context,
	pageNum int,
	cols []KeysetColumn,
	order []types.OrderField,
	userF types.Filter,
	limit int,
	r Reader[T, D],
) (*types.Page[D], error) {
	if pageNum < 1 {
		pageNum = 1
	}
	filters := nonEmpty(userF)
	rows, err := r.Runner.List(ctx, types.ListSpec{
		Filters: filters,
		Order:   order,
		Limit:   limit + 1,
		Offset:  (pageNum - 1) * limit,
	})
	if err != nil {
		return nil, r.MapErr(err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	var total int64
	var estimate bool
	// A count-cache decorator that already holds the total sets WithSkipCount on its
	// inner call, so Run skips the (otherwise recomputed) bounded count and the decorator
	// injects the cached value.
	if r.Count && !CountSkipped(ctx) {
		if total, estimate, err = boundedTotal(ctx, filters, r); err != nil {
			return nil, err
		}
	}
	next, err := offsetNextCursor(hasMore, pageNum, cols, rows, r)
	if err != nil {
		return nil, err
	}
	items, err := mapAll(rows, r.ToDomain)
	if err != nil {
		return nil, err
	}
	return &types.Page[D]{
		Items:           items,
		NextCursor:      next,
		Total:           total,
		TotalIsEstimate: estimate,
		PageSize:        limit,
		PageNumber:      pageNum,
	}, nil
}

// boundedTotal computes the offset pager's total_count, bounded at the reader's count
// cap. It asks the runner to tally matching rows up to cap+1; a tally within the cap is
// the exact total, while a tally over the cap is reported AS the cap with estimate=true —
// so the pager renders "N+" and a huge, unfiltered set never triggers a full-table COUNT
// on a per-page read.
func boundedTotal[T, D any](
	ctx context.Context,
	filters []types.Filter,
	r Reader[T, D],
) (total int64, isEstimate bool, err error) {
	capN := r.Bounds.countCap()
	n, err := r.Runner.Count(ctx, filters, capN+1)
	if err != nil {
		return 0, false, r.MapErr(err)
	}
	if n > capN {
		return int64(capN), true, nil
	}
	return int64(n), false, nil
}

// keysetPage serves the keyset (infinite-scroll) arm: it verifies the cursor fingerprint,
// builds the forward seek filter (AND-ed with the user filter), fetches limit+1 rows past
// the cursor, and mints the next keyset cursor from the last row. A keyset page has no
// page number and no total.
func keysetPage[T, D any](
	ctx context.Context,
	cur types.KeysetCursor,
	cols []KeysetColumn,
	order []types.OrderField,
	userF types.Filter,
	limit int,
	r Reader[T, D],
) (*types.Page[D], error) {
	if !bytes.Equal(cur.Fingerprint, types.Fingerprint(r.Sort, r.Filter)) {
		return nil, apperr.InvalidInput(
			"page token does not match the current query (sort or filter changed)",
		)
	}
	values, err := cursorValues(r.Keyset, cols, cur)
	if err != nil {
		return nil, err
	}
	rows, err := r.Runner.List(ctx, types.ListSpec{
		Filters: append(nonEmpty(userF), SeekFilter(cols, values)),
		Order:   order,
		Limit:   limit + 1,
	})
	if err != nil {
		return nil, r.MapErr(err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	next := ""
	if hasMore && len(rows) > 0 {
		if next, err = mintKeysetCursor(
			r.Keyset,
			cols,
			rows[len(rows)-1],
			r.Sort,
			r.Filter,
		); err != nil {
			return nil, err
		}
	}
	items, err := mapAll(rows, r.ToDomain)
	if err != nil {
		return nil, err
	}
	return &types.Page[D]{
		Items:      items,
		NextCursor: next,
		PageSize:   limit,
	}, nil
}

// cursorValues pairs the cursor's values with the ordered keyset columns and restores
// each to its bound type — the id via ParseID (e.g. uuid), a timestamp column from
// unix-µs to time.Time. A count mismatch is a malformed cursor (InvalidInput), never a
// panic.
func cursorValues[T any](
	ks *Keyset[T],
	cols []KeysetColumn,
	cur types.KeysetCursor,
) ([]any, error) {
	values := make([]any, len(cols))
	si := 0
	for i, c := range cols {
		if c.Column == KeysetIDColumn {
			id, err := parseID(ks, cur.ID)
			if err != nil {
				return nil, err
			}
			values[i] = id
			continue
		}
		if si >= len(cur.SortValues) {
			return nil, apperr.InvalidInput(
				"keyset cursor has too few sort values for the sort",
			)
		}
		v := cur.SortValues[si]
		si++
		if ks.TimestampCols[c.Column] {
			t, err := unixMicroToTime(v)
			if err != nil {
				return nil, err
			}
			v = t
		}
		values[i] = v
	}
	if si != len(cur.SortValues) {
		return nil, apperr.InvalidInput(
			"keyset cursor has too many sort values for the sort",
		)
	}
	return values, nil
}

// mintKeysetCursor encodes the forward keyset token for the page whose last row is
// lastRow: the row's value for each non-id keyset column plus its id, bound to the (sort
// ⊕ filter) fingerprint. The fingerprint is over the ORIGINAL (unresolved) sort + filter,
// which the next request recomputes. A timestamp value is carried as a unix-microsecond
// int64.
func mintKeysetCursor[T any](
	ks *Keyset[T],
	cols []KeysetColumn,
	lastRow T,
	sort []types.OrderField,
	filter types.Filter,
) (string, error) {
	sortValues := make([]any, 0, len(cols))
	var id string
	for _, c := range cols {
		v, ok := ks.RowValue(lastRow, c.Column)
		if !ok {
			return "", apperr.Internal("keyset cursor cannot extract column " + c.Column)
		}
		if c.Column == KeysetIDColumn {
			s, isStr := v.(string)
			if !isStr {
				return "", apperr.Internal("keyset id column value is not a string")
			}
			id = s
			continue
		}
		if ks.TimestampCols[c.Column] {
			t, isTime := v.(time.Time)
			if !isTime {
				return "", apperr.Internal(
					"keyset timestamp column value is not a time.Time",
				)
			}
			v = t.UnixMicro()
		}
		sortValues = append(sortValues, v)
	}
	return EncodeToken(types.PageToken{Keyset: &types.KeysetCursor{
		SortValues:  sortValues,
		ID:          id,
		Fingerprint: types.Fingerprint(sort, filter),
	}})
}

// offsetNextCursor returns the next-page token for an offset page. A keyset-wired reader
// ON ITS DEFAULT ORDERING mints a forward KEYSET cursor from the page's last row — so
// infinite scroll can bridge off the first (default-ordered) offset page (the numeric
// pager ignores it, using total_count + its own synthesized offset tokens). On a CUSTOM
// sort (no covering keyset index) it mints the next OFFSET page token instead, so a
// sorted list pages by navigation, not an un-indexed seek (curated set = default only).
// It is empty when no further rows remain.
func offsetNextCursor[T, D any](
	hasMore bool,
	pageNum int,
	cols []KeysetColumn,
	rows []T,
	r Reader[T, D],
) (string, error) {
	if !hasMore {
		return "", nil
	}
	if defaultOrdering(r) && len(rows) > 0 {
		return mintKeysetCursor(r.Keyset, cols, rows[len(rows)-1], r.Sort, r.Filter)
	}
	//nolint:gosec // G115: a 1-based page number is small and positive, well within int32.
	return EncodeToken(types.PageToken{OffsetPage: int32(pageNum + 1)})
}

// parseID converts a cursor id string to its bound value via the reader's ParseID (nil →
// the raw string), mapping a parse failure to InvalidInput so a malformed cursor resets
// cleanly.
func parseID[T any](ks *Keyset[T], id string) (any, error) {
	if ks.ParseID == nil {
		return id, nil
	}
	v, err := ks.ParseID(id)
	if err != nil {
		return nil, apperr.InvalidInput("keyset cursor id is malformed")
	}
	return v, nil
}

// orderFields converts the resolved keyset columns to the ListSpec ORDER BY (Field =
// storage column).
func orderFields(cols []KeysetColumn) []types.OrderField {
	out := make([]types.OrderField, len(cols))
	for i, c := range cols {
		// Carry a joined sort target, a value ordinal, and a derived-sort spec through to
		// the runner's FoldOrder so it renders the correlated-subquery / CASE / aggregate
		// ORDER BY; a plain column carries only its resolved Field.
		out[i] = types.OrderField{
			Field:   c.Column,
			Join:    c.Join,
			Ordinal: c.Ordinal,
			Derived: c.Derived,
			Desc:    c.Desc,
		}
	}
	return out
}

// nonEmpty returns [f] when f carries a real constraint, else nil — so an empty user
// filter is not nested into the query's predicate list.
func nonEmpty(f types.Filter) []types.Filter {
	if f == nil || f.IsEmpty() {
		return nil
	}
	return []types.Filter{f}
}

// unixMicroToTime converts a keyset timestamp cursor value — a unix-microsecond count
// carried as a JSON number (float64) or a raw int64 — back to a UTC time.Time. The
// instant is what the seek compares, so UTC vs local is immaterial; UTC keeps the bound
// parameter canonical.
func unixMicroToTime(v any) (time.Time, error) {
	switch n := v.(type) {
	case float64:
		return time.UnixMicro(int64(n)).UTC(), nil
	case int64:
		return time.UnixMicro(n).UTC(), nil
	case int:
		return time.UnixMicro(int64(n)).UTC(), nil
	default:
		return time.Time{}, apperr.InvalidInput(
			"keyset timestamp cursor value is not numeric",
		)
	}
}

// mapAll maps each row to its domain view, preserving order, stopping on the first
// mapping error (a data-integrity failure the mapper surfaces — e.g. an unknown enum from
// the database).
func mapAll[T, D any](rows []T, toDomain func(T) (D, error)) ([]D, error) {
	items := make([]D, len(rows))
	for i, r := range rows {
		d, err := toDomain(r)
		if err != nil {
			return nil, err
		}
		items[i] = d
	}
	return items, nil
}

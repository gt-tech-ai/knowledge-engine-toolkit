package cassandra

import (
	"context"
	"slices"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
)

// compactChunk is how many per-fact rows one compaction batch folds in: with the
// compacted row's upsert, a batch stays at 30 statements (the Keyspaces limit).
const compactChunk = 29

// groupRows are one (ts, dims_key) group of a partition.
type groupRows struct {
	// ts is the group's grain-aligned time.
	ts time.Time
	// compacted is the merged row's partials (nil before the first compaction).
	compacted map[string][]byte
	// dims are the group's dimension values.
	dims map[string]string
	// dimsKey is the group's stored clustering key.
	dimsKey string
	// keys are the per-fact rows' idempotency keys.
	keys []string
	// facts are the per-fact rows' partials, aligned with keys.
	facts []map[string][]byte
}

// Compact merges a closed bucket's per-fact rows into one row per (ts, dims_key)
// with an empty idempotency key. Each batch upserts the merged row and deletes the
// per-fact rows it absorbed, inside one partition, so it applies atomically and a
// half-done compaction never changes an answer; rerunning it is a no-op. Run one
// compactor per bucket, only for buckets past the facts' redelivery horizon (a
// fact redelivered after compaction would be counted again). With a TTL
// configured for the grain, the merged row keeps the bucket's retention — it
// expires at bucket end + TTL, never later — and a bucket already past that is
// left to expire on its own (nil, no I/O). An undeclared cube or grain, a
// misaligned bucket, or a bucket that has not ended is CodeInvalidInput.
func (s *Store) Compact(
	ctx context.Context,
	cube string,
	grain types.Grain,
	org string,
	bucket time.Time,
) error {
	bucket = bucket.UTC()

	now := time.Now()

	end, err := s.closedBucketEnd(cube, grain, bucket, now)
	if err != nil {
		return err
	}

	ttlSeconds, expired := remainingTTL(s.cfg.TTL[grain], end, now)
	if expired {
		return nil
	}

	session, err := s.sess()
	if err != nil {
		return err
	}

	groups, err := s.readPartition(ctx, session, cube, grain, org, bucket)
	if err != nil {
		return err
	}

	for _, g := range groups {
		err := compactGroup(ctx, session, cube, grain, org, bucket, ttlSeconds, g)
		if err != nil {
			return err
		}
	}

	return nil
}

// closedBucketEnd returns the bucket's end, rejecting an undeclared cube or grain and
// a bucket that is misaligned or still open.
func (s *Store) closedBucketEnd(
	cube string, grain types.Grain, bucket, now time.Time,
) (time.Time, error) {
	grains, ok := s.cfg.Cubes[cube]
	if !ok || !slices.Contains(grains, grain) {
		return time.Time{}, apperr.New(
			apperr.CodeInvalidInput,
			"analytics: undeclared cube or grain",
		)
	}

	width := s.cfg.BucketWidth[grain]
	end := chartspec.Next(bucket, width)

	if !chartspec.Truncate(bucket, width).Equal(bucket) || end.After(now) {
		return time.Time{}, apperr.New(
			apperr.CodeInvalidInput,
			"analytics: only a closed, aligned bucket can be compacted",
		)
	}

	return end, nil
}

// remainingTTL returns the seconds a bucket ending at end has left (0 means no TTL);
// expired reports a bucket already past its TTL.
func remainingTTL(ttl time.Duration, end, now time.Time) (seconds int, expired bool) {
	if ttl <= 0 {
		return 0, false
	}

	seconds = int(end.Add(ttl).Sub(now) / time.Second)

	return seconds, seconds < 1
}

// readPartition reads every row of one partition, grouped by (ts, dims_key) in
// clustering order.
func (s *Store) readPartition(
	ctx context.Context,
	session interfaces.CQLSession,
	cube string,
	grain types.Grain,
	org string,
	bucket time.Time,
) ([]*groupRows, error) {
	collected := &partitionGroups{index: map[string]*groupRows{}, groups: nil}

	var state []byte

	for {
		iter := session.Query(
			"SELECT ts, dims_key, idempotency_key, dims, partials FROM "+TableName(
				cube,
				grain,
			)+
				" WHERE org_id = ? AND cube = ? AND bucket = ?",
			org,
			cube,
			bucket,
		).WithContext(ctx).PageSize(s.cfg.PageSize).PageState(state).Idempotent(true).Iter()

		collected.scan(iter)

		state = iter.PageState()

		err := iter.Close()
		if err != nil {
			return nil, err
		}

		if len(state) == 0 {
			return collected.groups, nil
		}
	}
}

// partitionGroups collects a partition's rows by (ts, dims_key), in read order.
type partitionGroups struct {
	// index finds a group by its identity.
	index map[string]*groupRows

	// groups holds the groups in first-seen order.
	groups []*groupRows
}

// scan files every row of one page.
func (p *partitionGroups) scan(iter interfaces.CQLIter) {
	var (
		instant      time.Time
		dimsKey, key string
		dims         map[string]string
		partials     map[string][]byte
	)

	for iter.Scan(&instant, &dimsKey, &key, &dims, &partials) {
		secondGroupRows := p.group(instant.UTC(), dimsKey, dims)

		if key == "" {
			secondGroupRows.compacted = partials
		} else {
			secondGroupRows.keys = append(secondGroupRows.keys, key)
			secondGroupRows.facts = append(secondGroupRows.facts, partials)
		}

		instant, dimsKey, key, dims, partials = time.Time{}, "", "", nil, nil
	}
}

// group returns the (ts, dims_key) group, creating it on first sight.
func (p *partitionGroups) group(
	instant time.Time, dimsKey string, dims map[string]string,
) *groupRows {
	identifier2 := instant.Format(time.RFC3339Nano) + "|" + dimsKey

	currentGroupRows, ok := p.index[identifier2]
	if !ok {
		currentGroupRows = &groupRows{
			ts:        instant,
			dims:      dims,
			dimsKey:   dimsKey,
			compacted: nil,
			keys:      nil,
			facts:     nil,
		}
		p.index[identifier2] = currentGroupRows
		p.groups = append(p.groups, currentGroupRows)
	}

	return currentGroupRows
}

// compactGroup folds a group's per-fact rows into its compacted row, a chunk per
// batch, writing the merged row USING TTL ttlSeconds when it is positive.
func compactGroup(
	ctx context.Context,
	session interfaces.CQLSession,
	cube string,
	grain types.Grain,
	org string,
	bucket time.Time,
	ttlSeconds int,
	currentGroupRows *groupRows,
) error {
	table := TableName(cube, grain)
	dimsKey := currentGroupRows.dimsKey
	upsert, ttlArgs := compactUpsert(table, ttlSeconds)

	merged := currentGroupRows.compacted
	for start := 0; start < len(currentGroupRows.keys); start += compactChunk {
		end := min(start+compactChunk, len(currentGroupRows.keys))

		next, err := mergePartials(merged, currentGroupRows.facts[start:end])
		if err != nil {
			return err
		}

		batch := session.Batch(interfaces.CQLLoggedBatch).WithContext(ctx)
		batch.Query(
			upsert,
			append(
				[]any{
					org,
					cube,
					bucket,
					currentGroupRows.ts,
					dimsKey,
					currentGroupRows.dims,
					next,
				},
				ttlArgs...)...)

		for _, key := range currentGroupRows.keys[start:end] {
			batch.Query("DELETE FROM "+table+
				" WHERE org_id = ? AND cube = ? AND bucket = ? AND ts = ?"+
				" AND dims_key = ? AND idempotency_key = ?",
				org, cube, bucket, currentGroupRows.ts, dimsKey, key)
		}

		err = session.ExecuteBatch(batch)
		if err != nil {
			return err
		}

		merged = next
	}

	return nil
}

// compactUpsert is the statement that writes a compacted row, with its TTL arguments.
func compactUpsert(table string, ttlSeconds int) (cql string, binds []any) {
	upsert := "INSERT INTO " + table +
		" (org_id, cube, bucket, ts, dims_key, idempotency_key, dims, partials)" +
		" VALUES (?, ?, ?, ?, ?, '', ?, ?)"

	if ttlSeconds <= 0 {
		return upsert, nil
	}

	return upsert + " USING TTL ?", []any{ttlSeconds}
}

// mergePartials merges base and every row's partials, measure by measure.
func mergePartials(
	base map[string][]byte,
	rows []map[string][]byte,
) (map[string][]byte, error) {
	acc := map[string]chartspec.Partial{}

	err := accumulatePartials(acc, base)
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		err := accumulatePartials(acc, row)
		if err != nil {
			return nil, err
		}
	}

	out := make(map[string][]byte, len(acc))
	for name, p := range acc {
		out[name] = chartspec.EncodePartial(p)
	}

	return out, nil
}

// accumulatePartials merges one row's encoded partials into acc.
func accumulatePartials(
	acc map[string]chartspec.Partial,
	partials map[string][]byte,
) error {
	for name, blob := range partials {
		partial, err := chartspec.DecodePartial(blob)
		if err != nil {
			return err
		}

		cur := acc[name]
		cur.Merge(partial)

		acc[name] = cur
	}

	return nil
}

// Package outboxtest is the reusable OutboxStore conformance suite plus a
// reference database/sql Postgres store it is proven against. The reference
// store and its table are test-only: no product outbox table ships in the engine,
// and a consumer runs Run against its own store.
package outboxtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Schema is the reference store's DDL (Postgres). A row is pending until it is
// sent or parked; it is due once next_attempt_at has passed.
const Schema = `
CREATE TABLE IF NOT EXISTS outbox (
	id              uuid        PRIMARY KEY,
	lane            text        NOT NULL,
	tenant          text        NOT NULL,
	key             text        NOT NULL,
	payload         bytea       NOT NULL,
	attributes      jsonb       NOT NULL DEFAULT '{}',
	attempts        integer     NOT NULL DEFAULT 0,
	state           text        NOT NULL DEFAULT 'pending',
	last_error      text        NOT NULL DEFAULT '',
	created_at      timestamptz NOT NULL DEFAULT now(),
	next_attempt_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS outbox_due ON outbox (lane, next_attempt_at)
	WHERE state = 'pending';`

// claimSQL leases up to $2 due pending rows of lane $1 for $3 seconds, bumping
// each row's attempt count; SKIP LOCKED keeps concurrent claims disjoint.
const claimSQL = `
UPDATE outbox SET attempts = attempts + 1,
	next_attempt_at = now() + make_interval(secs => $3)
WHERE id IN (
	SELECT id FROM outbox
	WHERE lane = $1 AND state = 'pending' AND next_attempt_at <= now()
	ORDER BY created_at, id
	LIMIT $2
	FOR UPDATE SKIP LOCKED)
RETURNING id, lane, tenant, key, payload, attributes, attempts, created_at`

// SQLStore is the reference OutboxStore over database/sql (Postgres).
type SQLStore struct {
	// db is the database handle (owned by the caller).
	db *sql.DB
	// lease is how long a claimed row stays hidden from other claims.
	lease time.Duration
}

// NewSQLStore returns the reference store over db; a claimed row is leased for
// lease before it can be claimed again.
func NewSQLStore(db *sql.DB, lease time.Duration) *SQLStore {
	return &SQLStore{db: db, lease: lease}
}

// Migrate creates the reference table.
func (s *SQLStore) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, Schema); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "outboxtest: migrate")
	}
	return nil
}

// Enqueue inserts rec as a pending row due now (what a producer does inside its
// business transaction).
func (s *SQLStore) Enqueue(ctx context.Context, rec *types.OutboxRecord) error {
	attrs, err := json.Marshal(rec.Attributes)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"outboxtest: encode attributes",
		)
	}
	created := rec.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO outbox (id, lane, tenant, key, payload, attributes, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		rec.ID,
		rec.Lane,
		rec.Tenant,
		rec.Key,
		rec.Payload,
		attrs,
		created,
	)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "outboxtest: enqueue")
	}
	return nil
}

// Claim leases up to limit due pending rows of lane.
func (s *SQLStore) Claim(
	ctx context.Context,
	lane string,
	limit int,
) ([]types.OutboxRecord, error) {
	rows, err := s.db.QueryContext(ctx, claimSQL, lane, limit, s.lease.Seconds())
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeUnavailable, "outboxtest: claim")
	}
	defer rows.Close()
	var out []types.OutboxRecord
	for rows.Next() {
		var (
			rec   types.OutboxRecord
			attrs []byte
		)
		if err := rows.Scan(
			&rec.ID,
			&rec.Lane,
			&rec.Tenant,
			&rec.Key,
			&rec.Payload,
			&attrs,
			&rec.Attempts,
			&rec.CreatedAt,
		); err != nil {
			return nil, apperr.Wrap(err, apperr.CodeInternal, "outboxtest: scan claim")
		}
		if err := json.Unmarshal(attrs, &rec.Attributes); err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInternal,
				"outboxtest: decode attributes",
			)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeUnavailable, "outboxtest: claim rows")
	}
	return out, nil
}

// MarkSent records a delivered row.
func (s *SQLStore) MarkSent(ctx context.Context, id uuid.UUID) error {
	return s.exec(
		ctx,
		"mark sent",
		`UPDATE outbox SET state = 'sent', last_error = '' WHERE id = $1`,
		id,
	)
}

// Retry reschedules a pending row for next with its last error.
func (s *SQLStore) Retry(
	ctx context.Context,
	id uuid.UUID,
	next time.Time,
	lastErr string,
) error {
	return s.exec(
		ctx,
		"retry",
		`UPDATE outbox SET next_attempt_at = $2, last_error = $3
		WHERE id = $1 AND state = 'pending'`,
		id,
		next,
		lastErr,
	)
}

// Park removes a row from delivery, keeping its last error.
func (s *SQLStore) Park(ctx context.Context, id uuid.UUID, lastErr string) error {
	return s.exec(
		ctx,
		"park",
		`UPDATE outbox SET state = 'parked', last_error = $2 WHERE id = $1`,
		id,
		lastErr,
	)
}

// Stats counts lane's pending and parked rows and the oldest pending row's age.
func (s *SQLStore) Stats(ctx context.Context, lane string) (types.OutboxStats, error) {
	var (
		stats  types.OutboxStats
		oldest sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE state = 'pending'),
       count(*) FILTER (WHERE state = 'parked'),
       min(created_at) FILTER (WHERE state = 'pending')
FROM outbox WHERE lane = $1`, lane).Scan(&stats.Pending, &stats.Parked, &oldest)
	if err != nil {
		return types.OutboxStats{}, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"outboxtest: stats",
		)
	}
	if oldest.Valid {
		stats.OldestPending = oldest.Time
	}
	return stats, nil
}

// LastError returns a row's state and last error (for assertions).
func (s *SQLStore) LastError(
	ctx context.Context,
	id uuid.UUID,
) (state, lastErr string, err error) {
	err = s.db.QueryRowContext(
		ctx, `SELECT state, last_error FROM outbox WHERE id = $1`, id,
	).
		Scan(&state, &lastErr)
	if err != nil {
		return "", "", apperr.Wrap(err, apperr.CodeNotFound, "outboxtest: row")
	}
	return state, lastErr, nil
}

// exec runs one single-row update, reporting a missing row as CodeNotFound.
func (s *SQLStore) exec(ctx context.Context, op, query string, args ...any) error {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "outboxtest: "+op)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return apperr.New(
			apperr.CodeNotFound,
			"outboxtest: "+op+": no such pending row",
		)
	}
	return nil
}

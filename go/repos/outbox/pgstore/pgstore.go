// Package pgstore is the PostgreSQL OutboxStore: the relay's claim, lease and finalize
// operations over a product's own outbox table, named by configuration. Every product
// outbox table shares one column layout (below), so a service gets a production store by
// naming its table rather than re-writing the SKIP LOCKED claim per table.
//
// The table must carry these columns:
//
//	id uuid, lane text, payload bytea, status text|enum ('pending','sent','parked'),
//	attempts int, next_attempt_at timestamptz, last_error text, created_at timestamptz,
//	sent_at timestamptz NULL
//
// plus the key column (Config.KeyColumn), and optionally a tenant column and any
// attribute columns. Producers insert pending rows themselves, inside their business
// transaction; this store only drains them.
package pgstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Compile-time check: a Store is an OutboxStore.
var _ interfaces.OutboxStore = (*Store)(nil)

// Config names a store's table and the columns that vary between outbox tables.
type Config struct {
	// AttributeColumns maps each record attribute key to the column it is read from
	// (e.g. {"event_type": "event_type"}); empty reads no attributes.
	AttributeColumns map[string]string `yaml:"attribute_columns" mapstructure:"attribute_columns"`

	// Table is the outbox table's name.
	Table string `yaml:"table" mapstructure:"table"`

	// KeyColumn is the column a record's Key is read from (as text); empty means
	// "aggregate_id".
	KeyColumn string `yaml:"key_column" mapstructure:"key_column"`

	// TenantColumn is the column a record's Tenant is read from; empty means the table
	// has none and every record's Tenant is "".
	TenantColumn string `yaml:"tenant_column" mapstructure:"tenant_column"`

	// Lease is how long a claimed but unfinalized row is hidden from another claim. It
	// must outlast a relay pass, yet be short enough that a crashed relay's rows come
	// back promptly; it must be positive.
	Lease time.Duration `yaml:"lease" mapstructure:"lease"`

	// MaxErrorLen caps the recorded last_error, so a long delivery error cannot fail
	// the very update that records it; zero records it whole.
	MaxErrorLen int `yaml:"max_error_len" mapstructure:"max_error_len"`
}

// Validate rejects a non-identifier table or column and a non-positive lease with
// CodeInvalidInput.
func (c Config) Validate() error {
	names := []string{c.Table, c.keyColumn()}
	if c.TenantColumn != "" {
		names = append(names, c.TenantColumn)
	}
	for _, col := range c.AttributeColumns {
		names = append(names, col)
	}
	for _, n := range names {
		if !isIdentifier(n) {
			return apperr.InvalidInput(
				fmt.Sprintf("pgstore: %q is not a plain identifier", n),
			)
		}
	}
	if c.Lease <= 0 {
		return apperr.InvalidInput("pgstore: lease must be positive")
	}
	if c.MaxErrorLen < 0 {
		return apperr.InvalidInput("pgstore: max_error_len must not be negative")
	}
	return nil
}

// isIdentifier reports whether n is a plain lowercase SQL identifier (a letter or
// underscore, then letters, digits or underscores, at most 63 bytes). The table and
// column names are interpolated into the statements, so nothing else is accepted.
func isIdentifier(n string) bool {
	if n == "" || len(n) > 63 || (n[0] >= '0' && n[0] <= '9') {
		return false
	}
	for _, r := range n {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// keyColumn is KeyColumn with its default.
func (c Config) keyColumn() string {
	if c.KeyColumn == "" {
		return "aggregate_id"
	}
	return c.KeyColumn
}

// Store is the OutboxStore over one PostgreSQL outbox table.
type Store struct {
	db          *sql.DB
	claimSQL    string
	statsSQL    string
	sentSQL     string
	retrySQL    string
	parkSQL     string
	purgeSQL    string
	attrKeys    []string
	maxErrorLen int
	lease       time.Duration
}

// New returns the store for cfg over db. An invalid cfg is CodeInvalidInput; a nil db
// panics, as a wiring bug. It performs no I/O.
func New(db *sql.DB, cfg Config) (*Store, error) {
	if db == nil {
		panic("pgstore: db must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	tenant := "''"
	if cfg.TenantColumn != "" {
		tenant = cfg.TenantColumn
	}
	s := &Store{db: db, lease: cfg.Lease, maxErrorLen: cfg.MaxErrorLen}
	attrCols := ""
	var attrColsSb137 strings.Builder
	for key, col := range cfg.AttributeColumns {
		s.attrKeys = append(s.attrKeys, key)
		attrColsSb137.WriteString(", " + col + "::text")
	}
	attrCols += attrColsSb137.String()
	t := cfg.Table
	// The claim locks a batch of due rows (SKIP LOCKED keeps concurrent claims
	// disjoint), counts the attempt and pushes next_attempt_at out by the lease, all on
	// the database clock, so replica clock skew cannot re-claim a live lease.
	s.claimSQL = fmt.Sprintf(`
UPDATE %[1]s SET attempts = attempts + 1,
	next_attempt_at = now() + make_interval(secs => $3)
WHERE id IN (
	SELECT id FROM %[1]s
	WHERE lane = $1 AND status = 'pending' AND next_attempt_at <= now()
	ORDER BY created_at, id
	LIMIT $2
	FOR UPDATE SKIP LOCKED)
RETURNING id, lane, %[2]s, %[3]s::text, payload, attempts, created_at%[4]s`,
		t, tenant, cfg.keyColumn(), attrCols)
	s.statsSQL = fmt.Sprintf(`
SELECT count(*) FILTER (WHERE status = 'pending'),
       count(*) FILTER (WHERE status = 'parked'),
       min(created_at) FILTER (WHERE status = 'pending')
FROM %s WHERE lane = $1`, t)
	s.sentSQL = fmt.Sprintf(
		`UPDATE %s SET status = 'sent', sent_at = now(), last_error = ''
WHERE id = $1 AND status = 'pending'`,
		t,
	)
	s.retrySQL = fmt.Sprintf(`UPDATE %s SET next_attempt_at = $2, last_error = $3
WHERE id = $1 AND status = 'pending'`, t)
	s.parkSQL = fmt.Sprintf(`UPDATE %s SET status = 'parked', last_error = $2
WHERE id = $1 AND status = 'pending'`, t)
	s.purgeSQL = fmt.Sprintf(`DELETE FROM %s WHERE status = 'sent' AND sent_at < $1`, t)
	return s, nil
}

// Claim leases up to limit due pending rows of lane, oldest first; the returned
// Attempts counts this claim. A non-positive limit is CodeInvalidInput, since LIMIT 0
// would silently claim nothing every pass.
func (s *Store) Claim(
	ctx context.Context,
	lane string,
	limit int,
) ([]types.OutboxRecord, error) {
	if limit <= 0 {
		return nil, apperr.InvalidInput("pgstore: claim limit must be > 0")
	}
	rows, err := s.db.QueryContext(ctx, s.claimSQL, lane, limit, s.lease.Seconds())
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeUnavailable, "pgstore: claim")
	}
	defer func() { _ = rows.Close() }()
	var out []types.OutboxRecord
	for rows.Next() {
		rec, err := s.scanClaimed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeUnavailable, "pgstore: claim rows")
	}
	return out, nil
}

// scanClaimed reads one claimed row into a record, its attribute columns into
// Attributes.
func (s *Store) scanClaimed(rows *sql.Rows) (types.OutboxRecord, error) {
	var rec types.OutboxRecord
	attrs := make([]sql.NullString, len(s.attrKeys))
	dest := make([]any, 0, 7+len(attrs))
	dest = append(dest,
		&rec.ID,
		&rec.Lane,
		&rec.Tenant,
		&rec.Key,
		&rec.Payload,
		&rec.Attempts,
		&rec.CreatedAt,
	)
	for i := range attrs {
		dest = append(dest, &attrs[i])
	}
	if err := rows.Scan(dest...); err != nil {
		return types.OutboxRecord{}, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"pgstore: scan claim",
		)
	}
	if len(attrs) > 0 {
		rec.Attributes = make(map[string]string, len(attrs))
		for i, key := range s.attrKeys {
			rec.Attributes[key] = attrs[i].String
		}
	}
	return rec, nil
}

// MarkSent moves a pending row to sent, stamping sent_at on the database clock and
// clearing last_error. A row already sent or parked is left unchanged.
func (s *Store) MarkSent(ctx context.Context, id uuid.UUID) error {
	return s.exec(ctx, "mark sent", id, s.sentSQL, id)
}

// Retry makes a pending row due again at next and records lastErr. A row already sent
// or parked is left unchanged.
func (s *Store) Retry(
	ctx context.Context,
	id uuid.UUID,
	next time.Time,
	lastErr string,
) error {
	return s.exec(ctx, "retry", id, s.retrySQL, id, next, s.truncate(lastErr))
}

// Park takes a pending row out of rotation and records lastErr; the row stays for an
// operator to inspect. A row already sent is left unchanged.
func (s *Store) Park(ctx context.Context, id uuid.UUID, lastErr string) error {
	return s.exec(ctx, "park", id, s.parkSQL, id, s.truncate(lastErr))
}

// Stats counts lane's pending and parked rows and reports its oldest pending row's
// creation time, in one aggregate.
func (s *Store) Stats(ctx context.Context, lane string) (types.OutboxStats, error) {
	var (
		stats  types.OutboxStats
		oldest sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, s.statsSQL, lane).
		Scan(&stats.Pending, &stats.Parked, &oldest)
	if err != nil {
		return types.OutboxStats{}, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"pgstore: stats",
		)
	}
	if oldest.Valid {
		stats.OldestPending = oldest.Time
	}
	return stats, nil
}

// DeleteSentBefore purges sent rows whose sent_at is before cutoff and returns how many
// went; parked rows stay for an operator. The claim index covers only pending rows, so
// a retention sweep calls it to keep sent rows from accumulating.
func (s *Store) DeleteSentBefore(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, s.purgeSQL, cutoff)
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeUnavailable, "pgstore: delete sent")
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, apperr.Wrap(err, apperr.CodeInternal, "pgstore: delete sent count")
	}
	return int(n), nil
}

// exec runs one guarded single-row update. A row that is no longer pending is left
// unchanged and is not an error: another relay already finalized it.
func (s *Store) exec(
	ctx context.Context,
	op string,
	id uuid.UUID,
	query string,
	args ...any,
) error {
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "pgstore: "+op+" "+id.String())
	}
	return nil
}

// truncate keeps lastErr within MaxErrorLen; a rune cut in half is dropped.
func (s *Store) truncate(lastErr string) string {
	if s.maxErrorLen == 0 || len(lastErr) <= s.maxErrorLen {
		return lastErr
	}
	return strings.ToValidUTF8(lastErr[:s.maxErrorLen], "")
}

// Package postgres is the PostgreSQL kind of the outbox store (stores/outbox): the
// relay's claim, lease and finalize operations over a product's own outbox table, named
// by configuration. Every product outbox table shares one column layout (below), so a
// service gets a production store by naming its table rather than re-writing the SKIP
// LOCKED claim per table.
//
// The table must carry these columns:
//
//	id uuid, lane text, payload bytea, status text|enum ('pending','sent','parked'),
//	attempts int, next_attempt_at timestamptz, last_error text, created_at timestamptz,
//	sent_at timestamptz NULL
//
// plus the key column (Config.KeyColumn), and optionally a tenant column, a partition
// column and any attribute columns. Producers insert pending rows inside their business
// transaction, through Enqueue or their own INSERT; this store drains them.
//
// With Config.PartitionColumn a lane is split into Config.Partitions partitions: a
// row's partition is types.OutboxPartition(key, partitions), written at insert (Enqueue
// computes it; a product's own INSERT must use the same function) and never recomputed,
// and Claim leases only its partition's rows, so one relay per partition drains a lane
// in parallel. Lowering Partitions strands rows in the dropped partitions until they
// are rewritten, so a reduction needs a migration; Stats counts them (Stranded) so a
// relay host can refuse to start over them.
//
// Every write (Claim, the finalizers, DeleteSentBefore) runs on the *sql.Tx a
// transaction manager (stores/sqltx) bound to the context, and fails INTERNAL without
// touching the database when none is bound, so a write never autocommits outside the
// workflow that owns it. Stats reads on the bound transaction when there is one.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
)

// outboxFixedColumns counts the columns scanned before the attribute columns.
const outboxFixedColumns = 7

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

	// PartitionColumn is the integer column holding a row's partition; empty means the
	// table has none and every row is in partition 0.
	PartitionColumn string `yaml:"partition_column" mapstructure:"partition_column"`

	// Partitions is how many partitions a lane is split into; zero means 1. More than
	// one needs PartitionColumn.
	Partitions int `yaml:"partitions" mapstructure:"partitions"`

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
func (c *Config) Validate() error {
	err := c.validateIdentifiers()
	if err != nil {
		return err
	}

	if c.Lease <= 0 {
		return apperr.InvalidInput("outbox postgres store: lease must be positive")
	}

	if c.MaxErrorLen < 0 {
		return apperr.InvalidInput(
			"outbox postgres store: max_error_len must not be negative",
		)
	}

	if c.Partitions < 0 || (c.Partitions > 1 && c.PartitionColumn == "") {
		return apperr.InvalidInput(
			"outbox postgres store: partitions must not be negative, " +
				"and above 1 needs partition_column",
		)
	}

	return nil
}

// validateIdentifiers requires every configured table and column name to be plain.
func (c *Config) validateIdentifiers() error {
	names := []string{c.Table, c.keyColumn()}
	if c.TenantColumn != "" {
		names = append(names, c.TenantColumn)
	}

	if c.PartitionColumn != "" {
		names = append(names, c.PartitionColumn)
	}

	for _, col := range c.AttributeColumns {
		names = append(names, col)
	}

	for _, n := range names {
		if !isIdentifier(n) {
			return apperr.InvalidInput(
				fmt.Sprintf("outbox postgres store: %q is not a plain identifier", n),
			)
		}
	}

	return nil
}

// partitions is Partitions with its default of 1.
func (c *Config) partitions() int {
	return max(c.Partitions, 1)
}

// isIdentifier reports whether n is a plain lowercase SQL identifier (a letter or
// underscore, then letters, digits or underscores, at most 63 bytes). The table and
// column names are interpolated into the statements, so nothing else is accepted.
func isIdentifier(n string) bool {
	if n == "" || len(n) > maxIdentifierLen || isDigitRune(rune(n[0])) {
		return false
	}

	for _, r := range n {
		if !isIdentifierRune(r) {
			return false
		}
	}

	return true
}

// isDigitRune reports an ASCII digit.
func isDigitRune(r rune) bool { return r >= '0' && r <= '9' }

// isIdentifierRune reports a lowercase letter, digit or underscore.
func isIdentifierRune(
	r rune,
) bool {
	return (r >= 'a' && r <= 'z') || isDigitRune(r) || r == '_'
}

// keyColumn is KeyColumn with its default.
func (c *Config) keyColumn() string {
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
	enqueueSQL  string
	attrKeys    []string
	maxErrorLen int
	lease       time.Duration

	// partitions is how many partitions a lane is split into (1 without a column).
	partitions int

	// tenantColumn reports whether the table has a tenant column.
	tenantColumn bool

	// partitionColumn reports whether the table has a partition column.
	partitionColumn bool
}

// New returns the store for cfg over db. An invalid cfg is CodeInvalidInput; a nil db
// panics, as a wiring bug. It performs no I/O.
// cfg must not be nil.
func New(value *sql.DB, cfg *Config) (*Store, error) {
	if value == nil {
		panic("outbox postgres store: db must not be nil")
	}

	err := cfg.Validate()
	if err != nil {
		return nil, err
	}

	currentStore := &Store{
		db: value, lease: cfg.Lease, maxErrorLen: cfg.MaxErrorLen,
		tenantColumn: cfg.TenantColumn != "", partitions: cfg.partitions(),
		partitionColumn: cfg.PartitionColumn != "",
		claimSQL:        "",
		statsSQL:        "",
		sentSQL:         "",
		retrySQL:        "",
		parkSQL:         "",
		purgeSQL:        "",
		enqueueSQL:      "",
		attrKeys:        nil,
	}

	var attrCols strings.Builder

	for key, col := range cfg.AttributeColumns {
		currentStore.attrKeys = append(currentStore.attrKeys, key)

		_, _ = attrCols.WriteString(", " + col + "::text")
	}

	currentStore.prepareStatements(cfg, attrCols.String())

	return currentStore, nil
}

// claimSQL locks a batch of due rows (SKIP LOCKED keeps concurrent claims disjoint),
// counts the attempt and pushes next_attempt_at out by the lease, all on the database
// clock, so replica clock skew cannot re-claim a live lease. attrCols is the
// ", col::text" list of the attribute columns, in the store's attribute-key order.
func claimSQL(cfg *Config, attrCols string) string {
	tenant := "''"
	if cfg.TenantColumn != "" {
		tenant = cfg.TenantColumn
	}

	partition := ""
	if cfg.PartitionColumn != "" {
		partition = " AND " + cfg.PartitionColumn + " = $4"
	}

	return fmt.Sprintf(`
UPDATE %[1]s SET attempts = attempts + 1,
	next_attempt_at = now() + make_interval(secs => $3)
WHERE id IN (
	SELECT id FROM %[1]s
	WHERE lane = $1 AND status = 'pending' AND next_attempt_at <= now()%[5]s
	ORDER BY created_at, id
	LIMIT $2
	FOR UPDATE SKIP LOCKED)
RETURNING id, lane, %[2]s, %[3]s::text, payload, attempts, created_at%[4]s`,
		cfg.Table, tenant, cfg.keyColumn(), attrCols, partition)
}

// statsSQL is the lane summary: pending and parked counts, the oldest pending row's
// creation time, and the pending rows stranded at or above the partition count (the
// count is a validated int, so it is safe to interpolate).
func statsSQL(cfg *Config) string {
	stranded := "0"
	if cfg.PartitionColumn != "" {
		stranded = fmt.Sprintf(
			"count(*) FILTER (WHERE status = 'pending' AND %s >= %d)",
			cfg.PartitionColumn, cfg.partitions(),
		)
	}

	return fmt.Sprintf(`
SELECT count(*) FILTER (WHERE status = 'pending'),
       count(*) FILTER (WHERE status = 'parked'),
       min(created_at) FILTER (WHERE status = 'pending'),
       %s
FROM %s WHERE lane = $1`, stranded, cfg.Table)
}

// enqueueSQL is the producer's insert: a pending row due now (created now unless the
// record carries its creation time), its tenant, partition and attribute columns bound
// in that order after id, lane, payload, created_at and key; an id already present is
// left as is.
func enqueueSQL(cfg *Config, attrKeys []string) string {
	cols := []string{"id", "lane", "payload", "created_at", cfg.keyColumn()}
	if cfg.TenantColumn != "" {
		cols = append(cols, cfg.TenantColumn)
	}

	if cfg.PartitionColumn != "" {
		cols = append(cols, cfg.PartitionColumn)
	}

	for _, key := range attrKeys {
		cols = append(cols, cfg.AttributeColumns[key])
	}

	vals := make([]string, len(cols))
	for i := range cols {
		vals[i] = fmt.Sprintf("$%d", i+1)
	}

	vals[enqueuedAtColumn] = "COALESCE($4, now())"

	return fmt.Sprintf(`INSERT INTO %s
	(%s, status, attempts, next_attempt_at, last_error)
VALUES (%s, 'pending', 0, now(), '')
ON CONFLICT (id) DO NOTHING`,
		cfg.Table, strings.Join(cols, ", "), strings.Join(vals, ", "))
}

// Enqueue writes rec as a pending row due now on the transaction bound to ctx; see
// interfaces.OutboxWriter. An empty Key, Tenant or attribute is NULL; with a partition
// column the row's partition is types.OutboxPartition(rec.Key, partitions).
// rec must not be nil.
func (s *Store) Enqueue(ctx context.Context, rec *types.OutboxRecord) error {
	value, err := writeTx(ctx, "enqueue")
	if err != nil {
		return err
	}

	identifier := rec.ID
	if identifier == uuid.Nil {
		identifier = uuid.New()
	}

	var created any

	if !rec.CreatedAt.IsZero() {
		created = rec.CreatedAt
	}

	args := []any{identifier, rec.Lane, rec.Payload, created, nullable(rec.Key)}
	if s.tenantColumn {
		args = append(args, nullable(rec.Tenant))
	}

	if s.partitionColumn {
		args = append(args, types.OutboxPartition(rec.Key, s.partitions))
	}

	for _, key := range s.attrKeys {
		args = append(args, nullable(rec.Attributes[key]))
	}

	_, err = value.ExecContext(ctx, s.enqueueSQL, args...)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "outbox postgres store: enqueue")
	}

	return nil
}

// nullable binds v, or NULL when it is empty.
func nullable(v string) any {
	if v == "" {
		return nil
	}

	return v
}

// Claim leases up to limit due pending rows of lane's partition, oldest first; the
// returned Attempts counts this claim. A non-positive limit is CodeInvalidInput, since
// LIMIT 0 would silently claim nothing every pass, and so is a partition outside
// [0, partitions).
func (s *Store) Claim(
	ctx context.Context,
	lane string,
	partition, limit int,
) ([]types.OutboxRecord, error) {
	if limit <= 0 {
		return nil, apperr.InvalidInput("outbox postgres store: claim limit must be > 0")
	}

	if partition < 0 || partition >= s.partitions {
		return nil, apperr.InvalidInput(fmt.Sprintf(
			"outbox postgres store: partition %d is outside [0, %d)",
			partition,
			s.partitions,
		))
	}

	value, err := writeTx(ctx, "claim")
	if err != nil {
		return nil, err
	}

	args := []any{lane, limit, s.lease.Seconds()}
	if s.partitionColumn {
		args = append(args, partition)
	}

	rows, err := value.QueryContext(ctx, s.claimSQL, args...)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"outbox postgres store: claim",
		)
	}

	defer func() { _ = rows.Close() }()

	return s.collectClaimed(rows)
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

// Stats counts lane's pending, parked and stranded rows and reports its oldest
// pending row's creation time, in one aggregate.
func (s *Store) Stats(ctx context.Context, lane string) (types.OutboxStats, error) {
	var (
		stats  types.OutboxStats
		oldest sql.NullTime
	)

	row := s.db.QueryRowContext

	if tx, ok := store.ClientFrom[*sql.Tx](ctx); ok {
		row = tx.QueryRowContext
	}

	err := row(ctx, s.statsSQL, lane).
		Scan(&stats.Pending, &stats.Parked, &oldest, &stats.Stranded)
	if err != nil {
		return types.OutboxStats{}, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"outbox postgres store: stats",
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
	tx, err := writeTx(ctx, "delete sent")
	if err != nil {
		return 0, err
	}

	res, err := tx.ExecContext(ctx, s.purgeSQL, cutoff)
	if err != nil {
		return 0, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"outbox postgres store: delete sent",
		)
	}

	count, err := res.RowsAffected()
	if err != nil {
		return 0, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"outbox postgres store: delete sent count",
		)
	}

	return int(count), nil
}

// collectClaimed reads every claimed row into a record.
func (s *Store) collectClaimed(rows *sql.Rows) ([]types.OutboxRecord, error) {
	var out []types.OutboxRecord

	for rows.Next() {
		rec, err := s.scanClaimed(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, rec)
	}

	err := rows.Err()
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"outbox postgres store: claim rows",
		)
	}

	return out, nil
}

// scanClaimed reads one claimed row into a record, its attribute columns into
// Attributes.
func (s *Store) scanClaimed(rows *sql.Rows) (types.OutboxRecord, error) {
	var rec types.OutboxRecord

	attrs := make([]sql.NullString, len(s.attrKeys))
	dest := make([]any, 0, outboxFixedColumns+len(attrs))

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

	err := rows.Scan(dest...)
	if err != nil {
		return types.OutboxRecord{}, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"outbox postgres store: scan claim",
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

// exec runs one guarded single-row update. A row that is no longer pending is left
// unchanged and is not an error: another relay already finalized it.
func (*Store) exec(
	ctx context.Context,
	operation string,
	identifier uuid.UUID,
	query string,
	args ...any,
) error {
	tx, err := writeTx(ctx, operation)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, query, args...)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"outbox postgres store: "+operation+" "+identifier.String(),
		)
	}

	return nil
}

// writeTx returns the transaction a write runs on: the *sql.Tx bound to ctx. With none
// bound the write fails INTERNAL, since running it on the database would autocommit it
// outside the transaction its workflow opened.
func writeTx(ctx context.Context, op string) (*sql.Tx, error) {
	value, ok := store.ClientFrom[*sql.Tx](ctx)
	if !ok {
		return nil, apperr.New(
			apperr.CodeInternal,
			"outbox postgres store: "+op+" writes outside a transaction",
		)
	}

	return value, nil
}

// truncate keeps lastErr within MaxErrorLen; a rune cut in half is dropped.
func (s *Store) truncate(lastErr string) string {
	if s.maxErrorLen == 0 || len(lastErr) <= s.maxErrorLen {
		return lastErr
	}

	return strings.ToValidUTF8(lastErr[:s.maxErrorLen], "")
}

// prepareStatements renders every statement for cfg; attrCols selects the attribute
// columns.
func (s *Store) prepareStatements(cfg *Config, attrCols string) {
	text := cfg.Table

	s.claimSQL = claimSQL(cfg, attrCols)
	s.statsSQL = statsSQL(cfg)
	s.sentSQL = fmt.Sprintf(
		`UPDATE %s SET status = 'sent', sent_at = now(), last_error = ''
WHERE id = $1 AND status = 'pending'`,
		text,
	)
	s.retrySQL = fmt.Sprintf(
		`UPDATE %s SET next_attempt_at = $2, last_error = $3
WHERE id = $1 AND status = 'pending'`,
		text,
	)
	s.parkSQL = fmt.Sprintf(`UPDATE %s SET status = 'parked', last_error = $2
WHERE id = $1 AND status = 'pending'`, text)
	s.purgeSQL = fmt.Sprintf(
		`DELETE FROM %s WHERE status = 'sent' AND sent_at < $1`,
		text,
	)
	s.enqueueSQL = enqueueSQL(cfg, s.attrKeys)
}

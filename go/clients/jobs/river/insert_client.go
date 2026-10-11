package river

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// defaultInsertMaxConns caps an insert-only client's pgxpool. An enqueue-only producer
// never runs a worker loop, so it needs only a handful of connections; a caller sharing a
// Postgres connection budget with other pools can lower it further.
const defaultInsertMaxConns int32 = 4

// InsertClient is an insert-only River client: a capped pgxpool + a river.Client
// configured with NO queues or workers. It is the PRODUCER half a service (e.g. a
// request-serving one) uses to enqueue a job for the separate worker that consumes it —
// WITHOUT duplicating the pgxpool → riverpgxv5 → river.NewClient bring-up NewRuntime
// does, and without pulling the River worker runtime (or the pgx driver import) into the
// caller. Close releases the pool.
type InsertClient struct {
	// pool is the dedicated pgxpool the insert client runs on (closed by Close).
	pool *pgxpool.Pool
	// client is the underlying insert-only River client (no queues/workers configured).
	client *river.Client[pgx.Tx]
}

// NewInsertClient opens a capped pgxpool on databaseURL and builds an insert-only River
// client over it, failing loudly at composition time on a bad DSN so a misconfigured
// producer never silently drops jobs. maxConns caps the pool (non-positive →
// defaultInsertMaxConns). Close releases the pool.
func NewInsertClient(
	ctx context.Context,
	databaseURL string,
	maxConns int32,
) (*InsertClient, error) {
	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"river insert client: parse pool config",
		)
	}
	poolCfg.MaxConns = defaultInsertMaxConns
	if maxConns > 0 {
		poolCfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"river insert client: open pool",
		)
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
	if err != nil {
		pool.Close()
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"river insert client: new client",
		)
	}
	return &InsertClient{pool: pool, client: client}, nil
}

// Insert enqueues one job durably in River's own tables and reports whether a per-args
// uniqueness key made it a SKIP (an in-flight duplicate) rather than a new job. The
// caller passes its own concrete job-args type (any river.JobArgs). A failure is a coded
// (transient CodeUnavailable) error so the caller can classify retry-vs-fail.
func (c *InsertClient) Insert(
	ctx context.Context,
	args river.JobArgs,
) (skippedAsDuplicate bool, err error) {
	res, err := c.client.Insert(ctx, args, nil)
	if err != nil {
		return false, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"river insert client: insert job",
		)
	}
	return res.UniqueSkippedAsDuplicate, nil
}

// Close releases the pgxpool.
func (c *InsertClient) Close() { c.pool.Close() }

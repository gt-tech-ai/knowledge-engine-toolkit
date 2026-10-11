package river

import (
	"context"
	"maps"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// defaultMaxConns caps the River pgxpool conservatively. A caller that also
// holds other pools to the same Postgres must size them together so that
// replicas × (river + other pools) connections fit max_connections.
const defaultMaxConns int32 = 8

// defaultMaxWorkers is the per-queue, per-client concurrency when the caller
// does not set one.
const defaultMaxWorkers = 10

// RuntimeConfig configures the River runtime.
type RuntimeConfig struct {
	// Workers is the registry of job-kind handlers River dispatches to.
	Workers *river.Workers

	// ExtraQueues are ADDITIONAL named queues merged onto the base queue
	// (QueueName), each with its own MaxWorkers cap (per client, like
	// MaxWorkers). A worker that serves more than one job kind at DIFFERENT
	// concurrency ceilings (e.g. periodic sweeps on the default queue + heavy
	// jobs on a dedicated queue capped at MaxWorkers=K) supplies them here.
	ExtraQueues map[string]river.QueueConfig

	// DatabaseURL is the pgx connection string for the runtime's dedicated
	// pool.
	DatabaseURL string

	// QueueName is the base queue the runtime works; empty defaults to
	// river.QueueDefault.
	QueueName string

	// PeriodicJobs are scheduled jobs fired once cluster-wide via River leader
	// election.
	PeriodicJobs []*river.PeriodicJob

	// MaxWorkers caps concurrency on the base queue for THIS client (one
	// replica); non-positive uses defaultMaxWorkers. River applies the cap per
	// client, so fleet-wide concurrency is MaxWorkers × replicas — size it
	// from the downstream's capacity divided by the replica count.
	MaxWorkers int

	// MaxConns caps the pgxpool size; non-positive uses defaultMaxConns.
	MaxConns int32
}

// Runtime is the real River background-job runtime: a pgxpool-backed
// river.Client with its lifecycle (Start/Stop) and connection pool. Leader
// election (the river_leader table) is built in, so periodic jobs fire once
// cluster-wide across replicas sharing this database.
type Runtime struct {
	// pool is the dedicated pgxpool River runs on (closed by Stop).
	pool *pgxpool.Pool

	// client is the underlying River client.
	client *river.Client[pgx.Tx]
}

// NewRuntime builds a River runtime from the config, opening a capped pgxpool
// and constructing the River client with the caller's workers + periodic jobs
// on the default queue. It does not start processing — call Start.
func NewRuntime(ctx context.Context, cfg RuntimeConfig) (*Runtime, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"river runtime: parse pool config",
		)
	}
	poolCfg.MaxConns = defaultMaxConns
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "river runtime: open pool")
	}

	queues := buildQueues(cfg.QueueName, cfg.MaxWorkers)
	maps.Copy(queues, cfg.ExtraQueues)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:       queues,
		Workers:      cfg.Workers,
		PeriodicJobs: cfg.PeriodicJobs,
	})
	if err != nil {
		pool.Close()
		return nil, apperr.Wrap(err, apperr.CodeInternal, "river runtime: new client")
	}
	return &Runtime{pool: pool, client: client}, nil
}

// buildQueues maps the runtime onto its single queue: queueName (empty →
// river.QueueDefault) capped at maxWorkers (non-positive → defaultMaxWorkers). The cap is
// PER CLIENT (per replica), not global: K workers on a named queue across N replicas run
// up to K × N jobs of that kind at once. Pure so the construction is unit-tested without
// a database.
func buildQueues(queueName string, maxWorkers int) map[string]river.QueueConfig {
	if queueName == "" {
		queueName = river.QueueDefault
	}
	if maxWorkers <= 0 {
		maxWorkers = defaultMaxWorkers
	}
	return map[string]river.QueueConfig{queueName: {MaxWorkers: maxWorkers}}
}

// Start begins fetching and working jobs (and scheduling periodics on the leader).
func (r *Runtime) Start(ctx context.Context) error {
	if err := r.client.Start(ctx); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "river runtime: start")
	}
	return nil
}

// Stop gracefully drains in-flight jobs (honoring ctx for the deadline), then
// closes the connection pool.
func (r *Runtime) Stop(ctx context.Context) error {
	err := r.client.Stop(ctx)
	r.pool.Close()
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "river runtime: stop")
	}
	return nil
}

// Client exposes the underlying River client (e.g. for transactional enqueue).
func (r *Runtime) Client() *river.Client[pgx.Tx] { return r.client }

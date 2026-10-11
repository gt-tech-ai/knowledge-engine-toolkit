// Package postgres is the PostgreSQL backend of the database client tier: a
// lifecycle-managed *sql.DB pool (Client) plus ORM-independent pool utilities — a
// boot-time reachability probe (Ping) and a Prometheus pool-stats collector. It
// depends only on database/sql and core, so it wires into any composition root
// without pulling in an ORM.
package postgres

import (
	"context"
	"database/sql"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// pingTimeout bounds the startup connectivity probe so a down/unreachable DB
// fails fast at the composition root rather than hanging it.
const pingTimeout = 5 * time.Second

// StatsInterval is the default cadence at which the pool gauges refresh.
const StatsInterval = 15 * time.Second

// readinessTimeout bounds a single readiness ping so a slow/unreachable database
// flips /readyz to 503 quickly instead of hanging the probe. It is deliberately
// tighter than pingTimeout (the boot-time 5s fail-fast): Ping derives its deadline
// from the passed ctx (context.WithTimeout(ctx, pingTimeout)), so this 2s ctx wins
// as the earliest deadline — size the K8s readinessProbe timeoutSeconds to it.
const readinessTimeout = 2 * time.Second

// Check returns a readiness closure that pings db under a bounded (readinessTimeout)
// context, so a Kubernetes readiness probe can gate /readyz on the pool actually
// reaching Postgres: the closure returns nil while the
// database is reachable and a wrapped error once it is not, at which point the pod
// is pulled from the Service endpoints. Liveness must NOT use this — a slow DB
// should not restart the pod.
func Check(db *sql.DB) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), readinessTimeout)
		defer cancel()
		return Ping(ctx, db)
	}
}

// Ping verifies the pool can reach the database, so a bad DSN or a down server
// surfaces as a clear boot-time error at the composition root instead of as the
// first request's failure. Call it right after building the pool. A failure is
// coded CodeUnavailable: an unreachable database is transient, so callers that
// classify by code (IsTransient, the retry policy) may retry it.
func Ping(ctx context.Context, db *sql.DB) error {
	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"database ping failed (check DSN and connectivity)",
		)
	}
	return nil
}

// StartStatsCollector publishes the *sql.DB connection-pool stats as gauges on m
// and refreshes them every interval until ctx is cancelled. It returns
// immediately after publishing once, so the series exist before the first scrape.
// A non-positive interval falls back to StatsInterval.
//
// The gauge names are stable so a pool-saturation alert can be written against them
// (e.g. db_pool_active_connections / db_pool_max_connections > 0.8) — wiring this at
// each server's composition root is what makes such an alert resolve. "active"
// is the in-use count (connections currently serving a query); the pool nears
// saturation as in-use approaches the max, at which point new callers wait.
func StartStatsCollector(
	ctx context.Context,
	db *sql.DB,
	m interfaces.Metrics,
	interval time.Duration,
) {
	if interval <= 0 {
		interval = StatsInterval
	}

	maxConns := m.Gauge(
		"db_pool_max_connections",
		"Maximum number of open connections allowed in the pool",
	)
	active := m.Gauge(
		"db_pool_active_connections",
		"Connections currently in use (serving a query)",
	)
	open := m.Gauge(
		"db_pool_open_connections",
		"Connections currently open (in use + idle)",
	)
	idle := m.Gauge("db_pool_idle_connections", "Idle connections currently in the pool")
	waitCount := m.Gauge(
		"db_pool_wait_count",
		"Cumulative number of connection acquisitions that had to wait",
	)
	waitSeconds := m.Gauge(
		"db_pool_wait_duration_seconds",
		"Cumulative time blocked waiting for a connection",
	)

	publish := func() {
		s := db.Stats()
		maxConns.Set(float64(s.MaxOpenConnections))
		active.Set(float64(s.InUse))
		open.Set(float64(s.OpenConnections))
		idle.Set(float64(s.Idle))
		waitCount.Set(float64(s.WaitCount))
		waitSeconds.Set(s.WaitDuration.Seconds())
	}

	publish()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				publish()
			}
		}
	}()
}

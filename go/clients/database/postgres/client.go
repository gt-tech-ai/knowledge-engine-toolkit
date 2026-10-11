package postgres

import (
	"context"
	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver registration

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// Client is a lifecycle-managed database connection pool: Start opens + pings the
// pool from config, Stop closes it, and Liveness/Readiness answer Kubernetes
// probes. It satisfies interfaces.Client so a lifecycle.Manager coordinates its
// startup and shutdown; DB() exposes the shared *sql.DB the ORM adapters and the
// raw-SQL callers run over.
type Client struct {
	// cfg is the database configuration used to open and tune the pool.
	cfg *infra.DatabaseConfig
	// db is the shared connection pool, opened by Start and closed by Stop.
	db *sql.DB
}

// compile-time check: Client is a lifecycle-managed, health-checkable database
// pool (it satisfies interfaces.DatabasePool, which embeds interfaces.Client).
var _ interfaces.DatabasePool = (*Client)(nil)

// New builds a lifecycle-managed pool client from config. The pool is not opened
// until Start. It is the PostgreSQL backend constructor the database tier's
// NewFromConfig selects for KindPostgres.
func New(cfg *infra.DatabaseConfig) *Client {
	return &Client{cfg: cfg}
}

// Start opens the pool from config, applies the pool sizing, and pings to fail fast
// at the composition root rather than on the first query.
func (c *Client) Start(ctx context.Context) error {
	db, err := sql.Open("pgx", c.cfg.DSN())
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "open database")
	}
	db.SetMaxOpenConns(c.cfg.MaxConnections)
	db.SetMaxIdleConns(c.cfg.MaxIdleConnections)
	db.SetConnMaxLifetime(c.cfg.ConnectionMaxLifetime)
	db.SetConnMaxIdleTime(c.cfg.ConnMaxIdleTime)
	if err := Ping(ctx, db); err != nil {
		_ = db.Close()
		return err
	}
	c.db = db
	return nil
}

// Stop closes the pool and its connections.
func (c *Client) Stop(_ context.Context) error {
	if c.db != nil {
		return apperr.Wrap(
			c.db.Close(),
			apperr.CodeInternal,
			"close postgres pool",
		)
	}
	return nil
}

// Liveness reports whether the pool client has been started.
func (c *Client) Liveness(_ context.Context) error {
	if c.db == nil {
		return apperr.Internal("database pool not started")
	}
	return nil
}

// Readiness verifies the pool can reach the database (a bounded Ping).
func (c *Client) Readiness(ctx context.Context) error {
	if c.db == nil {
		return apperr.Internal("database pool not started")
	}
	return Ping(ctx, c.db)
}

// DB returns the underlying *sql.DB the ORM adapters and raw-SQL callers share.
func (c *Client) DB() *sql.DB {
	return c.db
}

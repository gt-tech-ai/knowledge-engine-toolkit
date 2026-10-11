// Package gorm is the GORM ORM adapter (repos/adapters/gorm): a lifecycle-managed
// opener for a *gorm.DB selected by Kind (lazily, in Start), so a consumer can put
// its repositories on GORM, or switch another persistence adapter it owns for this
// one, as a composition-root wiring change rather than a rewrite.
package gorm

import (
	"context"
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// Kind selects the SQL dialect the GORM adapter opens.
type Kind int

const (
	// KindPostgres opens a PostgreSQL connection via the GORM postgres driver.
	KindPostgres Kind = iota
)

// String returns the string form of Kind.
func (k Kind) String() string {
	switch k {
	case KindPostgres:
		return "postgres"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config configures the GORM adapter: the SQL dialect (Kind) and the connection
// settings.
type Config struct {
	// Database holds the connection settings (DSN, pool sizing).
	Database infra.DatabaseConfig

	// Kind selects the SQL dialect. The zero value is KindPostgres.
	Kind Kind
}

// Client is a GORM database handle a composition root can select by configuration.
// The pool (db) is opened lazily in Start — NewFromConfig does no I/O — the same
// "construct with zero I/O, dial at Start" contract as the clients/database/postgres
// backend.
type Client struct {
	// db is the underlying GORM connection pool. It is nil until Start opens it.
	db *gorm.DB
	// cfg holds the dialect (Kind) and connection settings used to dial in Start.
	cfg Config
}

// NewFromConfig validates cfg.Kind and returns an unopened GORM client. It is the
// adapter's app-wiring entrypoint and does no I/O: the connection is dialed in Start so
// a composition root can be assembled against an unreachable database without failing
// at graph-build time.
func NewFromConfig(cfg Config) (*Client, error) {
	if cfg.Kind != KindPostgres {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown gorm kind: %v", cfg.Kind),
		)
	}
	return &Client{cfg: cfg}, nil
}

// DB returns the underlying *gorm.DB for repositories to query through. It is nil
// until Start has opened the pool.
func (c *Client) DB() *gorm.DB { return c.db }

// Close closes the underlying connection pool (a no-op if never started).
func (c *Client) Close() error {
	if c.db == nil {
		return nil
	}
	return closePool(c.db)
}

// compile-time check: the GORM client is a lifecycle-managed, health-checkable
// client (interfaces.Client), so a lifecycle.Manager coordinates it uniformly with
// the other clients.
var _ interfaces.Client = (*Client)(nil)

// Start opens the connection pool and verifies it is reachable under ctx, failing
// fast at the composition root rather than on the first query. A failed Start keeps
// no pool: lifecycle.Manager never stops the client whose Start failed, so the pool
// is closed here and DB/Liveness keep reporting not started. GORM's own automatic
// ping is disabled so the reachability check honours ctx.
func (c *Client) Start(ctx context.Context) error {
	db, err := gorm.Open(
		postgres.Open(c.cfg.Database.DSN()),
		&gorm.Config{DisableAutomaticPing: true},
	)
	if err != nil {
		if db != nil {
			_ = closePool(db)
		}
		return apperr.Wrap(err, apperr.CodeInternal, "open gorm postgres")
	}
	if err := ping(ctx, db); err != nil {
		_ = closePool(db)
		return err
	}
	c.db = db
	return nil
}

// Stop closes the underlying connection pool.
func (c *Client) Stop(_ context.Context) error {
	return c.Close()
}

// Liveness reports whether the pool has been opened (a lightweight self-check that
// does not touch the database); it fails until Start has run.
func (c *Client) Liveness(_ context.Context) error {
	if c.db == nil {
		return apperr.Internal("gorm client not started")
	}
	return nil
}

// Readiness verifies the database can serve traffic (a Ping bounded by ctx); it
// fails until Start has opened the pool, and reports an unreachable database as
// CodeUnavailable.
func (c *Client) Readiness(ctx context.Context) error {
	if c.db == nil {
		return apperr.Internal("gorm client not started")
	}
	return ping(ctx, c.db)
}

// ping checks that db's pool reaches the database, coding a failure CodeUnavailable
// (a down or unreachable database is transient).
func ping(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "gorm sql.DB")
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"gorm postgres ping failed",
		)
	}
	return nil
}

// closePool closes db's underlying *sql.DB pool.
func closePool(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "gorm sql.DB")
	}
	if err := sqlDB.Close(); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "close gorm postgres pool")
	}
	return nil
}

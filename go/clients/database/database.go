// Package database is the database client tier: it selects a SQL connection-pool
// backend — PostgreSQL today (postgres/) — by Kind and returns the
// interfaces.DatabasePool contract, so switching database engines is a config
// change, not a caller edit. The contract lives in core (interfaces.DatabasePool);
// the backend implements it; this is the factory, mirroring the cache/storage
// NewFromConfig shape.
package database

import (
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/database/postgres"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// Kind selects the database backend.
type Kind int

const (
	// KindPostgres uses a PostgreSQL connection pool via the pgx stdlib driver
	// (production and dev).
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

// Config selects and configures the database backend.
type Config struct {
	// Database holds the connection settings (DSN, pool sizing) for the backend.
	Database *infra.DatabaseConfig

	// Kind selects the backend. The zero value is KindPostgres.
	Kind Kind
}

// NewFromConfig builds the database pool selected by cfg.Kind: the PostgreSQL pool
// (KindPostgres) today. It is the app-wiring entrypoint mirroring the cache/storage
// NewFromConfig factory; the returned pool is not opened until Start.
func NewFromConfig(cfg Config) (interfaces.DatabasePool, error) {
	switch cfg.Kind {
	case KindPostgres:
		return postgres.New(cfg.Database), nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown database kind: %v", cfg.Kind),
		)
	}
}

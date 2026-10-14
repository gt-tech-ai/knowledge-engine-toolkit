// Package database builds the SQL connection pool from config: PostgreSQL (postgres/)
// today, behind interfaces.DatabasePool, so switching engines is a config change. It
// ships no stub: a pool hands its caller a *sql.DB, which nothing zero-I/O can stand in
// for, so an empty kind selects PostgreSQL.
package database

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/database/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// KindPostgres pools connections to PostgreSQL (the default).
const KindPostgres client.Kind = "postgres"

// Config selects and tunes the pool.
type Config struct {
	// Kind selects the backend; empty means KindPostgres.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Database configures the connection.
	Database infra.DatabaseConfig `yaml:"database" mapstructure:"database"`
}

// Deps carries the pool's runtime collaborators (none today).
type Deps struct{}

// kinds is the database backend table.
func kinds() client.Kinds[Config, Deps, interfaces.DatabasePool] {
	return client.Kinds[Config, Deps, interfaces.DatabasePool]{
		Family:  "database",
		Default: KindPostgres,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.DatabasePool]{
			KindPostgres: func(
				_ context.Context, cfg Config, _ Deps,
			) (interfaces.DatabasePool, error) {
				return postgres.New(&cfg.Database), nil
			},
		},
	}
}

// NewFromConfig builds the pool cfg.Kind selects, without I/O (it connects on Start).
// An unknown kind is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.DatabasePool, error) {
	return kinds().New(ctx, cfg.Kind, cfg, deps)
}

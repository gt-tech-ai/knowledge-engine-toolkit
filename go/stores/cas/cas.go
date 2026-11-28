// Package cas builds the content-addressed stores from config: the insert-only node
// store and the compare-and-swap ref store a versioned record is kept in (see
// foundation/cas for the hashing). Each kind is a subpackage — stub/ (empty, the
// default, so a graph boots with no database) and postgres/ (two tables on a
// product's database) — and NewFromConfig returns both stores from one Config.
//
// The stores are not wrapped in the store tier: a caller that wants retry, tracing or
// metrics decorates the returned interfaces at its composition root.
package cas

import (
	"context"
	"database/sql"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/cas/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/cas/stub"
)

const (
	// KindStub is the empty store (the default).
	KindStub = client.KindStub

	// KindPostgres keeps nodes and refs in two PostgreSQL tables.
	KindPostgres client.Kind = "postgres"
)

// Config selects and tunes the cas stores.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the stores in errors and logs; empty means "cas".
	Name string `yaml:"name" mapstructure:"name"`

	// Postgres configures KindPostgres.
	Postgres postgres.Config `yaml:"postgres" mapstructure:"postgres"`
}

// Deps carries the cas stores' runtime collaborators.
type Deps struct {
	// DB is the database KindPostgres runs on.
	DB *sql.DB
}

// Stores is the pair of stores one Config builds.
type Stores struct {
	// Nodes is the insert-only node store.
	Nodes interfaces.NodeStore

	// Refs is the compare-and-swap ref store.
	Refs interfaces.RefStore
}

// kinds is the cas store backend table.
func kinds() client.Kinds[Config, Deps, Stores] {
	return client.Kinds[Config, Deps, Stores]{
		Family:  "cas store",
		Default: KindStub,
		Backends: map[client.Kind]client.Backend[Config, Deps, Stores]{
			KindStub: func(context.Context, Config, Deps) (Stores, error) {
				return Stores{Nodes: stub.Nodes{}, Refs: stub.Refs{}}, nil
			},
			KindPostgres: client.RefConfig(newPostgres),
		},
		Decorate:   nil,
		Options:    nil,
		AtMostOnce: nil,
	}
}

// NewFromConfig builds the stores cfg.Kind selects, with no I/O. An unknown kind, an
// invalid backend config or a postgres kind without a DB is CodeInvalidInput.
// cfg must not be nil.
func NewFromConfig(ctx context.Context, cfg *Config, deps Deps) (Stores, error) {
	return kinds().New(ctx, cfg.Kind, *cfg, deps)
}

// newPostgres builds the stores over cfg.Postgres's tables on deps.DB.
func newPostgres(_ context.Context, cfg *Config, deps Deps) (Stores, error) {
	if deps.DB == nil {
		return Stores{}, apperr.New(
			apperr.CodeInvalidInput,
			"cas store: postgres needs a DB",
		)
	}

	nodes, refs, err := postgres.New(deps.DB, &cfg.Postgres)
	if err != nil {
		return Stores{}, err
	}

	return Stores{Nodes: nodes, Refs: refs}, nil
}

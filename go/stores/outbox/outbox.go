// Package outbox builds the durable side of a transactional outbox from config: the
// store the relay (workflows/outbox) leases rows from and finalizes them in. Each kind is
// a subpackage — stub/ (no rows, the default, so a relay boots with no database) and
// postgres/ (a product's own outbox table) — and NewFromConfig returns the
// interfaces.OutboxStore behind the store tier (Decorate), as the decoration config's
// stores tier resolves it for Config.Name.
package outbox

import (
	"context"
	"database/sql"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox/postgres"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/outbox/stub"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
)

const (
	// KindStub is the empty outbox (the default).
	KindStub = client.KindStub

	// KindPostgres is a product's PostgreSQL outbox table.
	KindPostgres client.Kind = "postgres"
)

// Config selects and tunes the outbox store.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name is the store's name in the decoration config's stores tier (and its metric,
	// span and log label); empty means "outbox".
	Name string `yaml:"name" mapstructure:"name"`

	// Postgres configures KindPostgres.
	Postgres postgres.Config `yaml:"postgres" mapstructure:"postgres"`
}

// Deps carries the outbox store's runtime collaborators.
type Deps struct {
	// DB is the database KindPostgres runs on.
	DB *sql.DB

	// Decorations builds the store's layers from the stores tier of the decoration
	// config, under Config.Name.
	decorate.Decorations
}

// kinds is the outbox store backend table.
func kinds() client.Kinds[Config, Deps, interfaces.OutboxStore] {
	return client.Kinds[Config, Deps, interfaces.OutboxStore]{
		Family:  "outbox store",
		Default: KindStub,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.OutboxStore]{
			KindStub: func(context.Context, Config, Deps) (interfaces.OutboxStore, error) {
				return stub.New(), nil
			},
			KindPostgres: newPostgres,
		},
	}
}

// NewFromConfig builds the outbox store cfg.Kind selects, behind the store tier, with
// no I/O. An unknown kind, an invalid backend config, a postgres kind without a DB, or
// a decoration that fails to build is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.OutboxStore, error) {
	s, err := kinds().New(ctx, cfg.Kind, cfg, deps)
	if err != nil {
		return nil, err
	}
	name := cfg.Name
	if name == "" {
		name = "outbox"
	}
	b, err := store.NewBase[interfaces.OutboxStore](deps.Decorations, name, deps.DB)
	if err != nil {
		return nil, err
	}
	return b.Outbox(s), nil
}

// newPostgres builds the store over cfg.Postgres's table on deps.DB.
func newPostgres(
	_ context.Context,
	cfg Config,
	deps Deps,
) (interfaces.OutboxStore, error) {
	if deps.DB == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"outbox store: postgres needs a DB",
		)
	}
	return postgres.New(deps.DB, cfg.Postgres)
}

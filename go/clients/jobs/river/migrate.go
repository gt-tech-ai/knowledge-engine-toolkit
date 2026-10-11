package river

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Migrate applies River's own database schema (the river_job, river_leader, …
// tables the River runtime requires) to the database at databaseURL.
//
// River's tables are infrastructure River owns, not part of the caller's own
// schema, so they are applied with River's native migrator rather than the
// caller's schema-migration tool — keeping them out of that tool's drift check,
// which would otherwise report the river_* tables as perpetual drift. A River
// runtime cannot start until these tables exist, so a deployment's migration step
// invokes Migrate after its own schema apply.
//
// Migrate is idempotent: re-running it is a no-op once the schema is current.
func Migrate(ctx context.Context, databaseURL string) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "river migrate: open pool")
	}
	defer pool.Close()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "river migrate: new migrator")
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "river migrate: apply up")
	}
	return nil
}

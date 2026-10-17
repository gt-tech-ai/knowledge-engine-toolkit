// Package sqltx is the database/sql transaction manager: the
// interfaces.TransactionManager a workflow opens its transactions through when its
// stores run raw SQL on a *sql.DB (the postgres outbox store). It binds the open
// *sql.Tx to the context with store.WithClient, so a store finds it with
// store.ClientFrom[*sql.Tx], and marks the context in-transaction. A transaction never
// opens inside another: a context already in one fails INTERNAL.
package sqltx

import (
	"context"
	"database/sql"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
)

// Compile-time check: a Manager is a TransactionManager.
var _ interfaces.TransactionManager = (*Manager)(nil)

// Manager opens transactions on one *sql.DB.
type Manager struct {
	// db is the database transactions open on.
	db *sql.DB
}

// New returns the manager over db. A nil db panics, as a wiring bug. It performs no I/O.
func New(db *sql.DB) *Manager {
	if db == nil {
		panic("sqltx: db must not be nil")
	}
	return &Manager{db: db}
}

// Tx returns the *sql.Tx bound to ctx by a Manager, and whether one is.
func Tx(ctx context.Context) (*sql.Tx, bool) {
	return store.ClientFrom[*sql.Tx](ctx)
}

// Begin opens a transaction and returns ctx carrying it. The caller commits or rolls
// back the returned Transaction; prefer WithTransaction, which also runs after-commit
// hooks.
func (m *Manager) Begin(
	ctx context.Context,
) (context.Context, interfaces.Transaction, error) {
	tx, err := m.begin(ctx)
	if err != nil {
		return ctx, nil, err
	}
	return interfaces.WithInTx(store.WithClient(ctx, tx)), tx, nil
}

// WithTransaction runs fn in one transaction, which commits when fn succeeds and rolls
// back when it fails; after a commit it runs the hooks fn registered with
// interfaces.RegisterAfterCommit. fn's error is returned with its code intact.
func (m *Manager) WithTransaction(
	ctx context.Context,
	fn func(context.Context) error,
) error {
	tx, err := m.begin(ctx)
	if err != nil {
		return err
	}
	// A no-op after a successful commit, so it also covers a failed one.
	defer func() { _ = tx.Rollback() }()
	txCtx := interfaces.WithAfterCommit(interfaces.WithInTx(store.WithClient(ctx, tx)))
	if err := fn(txCtx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeOr(err, apperr.CodeUnavailable),
			"sqltx: commit",
		)
	}
	interfaces.RunAfterCommit(txCtx)
	return nil
}

// begin opens a transaction, refusing one on a context already in a transaction.
func (m *Manager) begin(ctx context.Context) (*sql.Tx, error) {
	if interfaces.InTx(ctx) {
		return nil, apperr.New(
			apperr.CodeInternal,
			"sqltx: a transaction is already open on this context",
		)
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeOr(err, apperr.CodeUnavailable),
			"sqltx: begin",
		)
	}
	return tx, nil
}

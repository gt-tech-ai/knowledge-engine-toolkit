// Package postgres is the PostgreSQL kind of the cas stores (stores/cas): the node
// store and the ref store over two tables, named by configuration (schema.sql holds
// their DDL under the default names).
//
// Nodes are insert-only: Put is INSERT … ON CONFLICT DO NOTHING, so a second put of a
// key never changes the stored body. When nothing is inserted the store reads the body
// it holds and reports Conflict if the two are not the same JSON value. Refs move by
// compare-and-swap: a move from a zero hash creates the ref with ON CONFLICT DO NOTHING,
// any other move is an UPDATE guarded by the hash the caller saw, and a statement that
// changes no row is a lost race (false, no error).
//
// Both stores run each statement on the *sql.Tx a transaction manager (stores/sqltx)
// bound to the context, so a caller can put nodes and move a ref in one transaction,
// and on the database otherwise: an insert-only put is safe to autocommit.
package postgres

import (
	"bytes"
	"context"
	"database/sql"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cas"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
)

// Compile-time checks: Nodes is a NodeStore and Refs a RefStore.
var (
	_ interfaces.NodeStore = (*Nodes)(nil)
	_ interfaces.RefStore  = (*Refs)(nil)
)

// Config names the two tables.
type Config struct {
	// NodesTable is the node table's name; empty means "cas_nodes".
	NodesTable string `yaml:"nodes_table" mapstructure:"nodes_table"`

	// RefsTable is the ref table's name; empty means "cas_refs".
	RefsTable string `yaml:"refs_table" mapstructure:"refs_table"`
}

// Validate rejects a table name that is not a plain lowercase identifier with
// CodeInvalidInput: the names are interpolated into the statements.
func (c *Config) Validate() error {
	for _, name := range []string{c.nodesTable(), c.refsTable()} {
		if !isIdentifier(name) {
			return apperr.InvalidInput(
				"cas postgres store: " + name + " is not a plain identifier",
			)
		}
	}

	return nil
}

// nodesTable is NodesTable with its default.
func (c *Config) nodesTable() string {
	if c.NodesTable == "" {
		return defaultNodesTable
	}

	return c.NodesTable
}

// refsTable is RefsTable with its default.
func (c *Config) refsTable() string {
	if c.RefsTable == "" {
		return defaultRefsTable
	}

	return c.RefsTable
}

// isIdentifier reports whether n is a plain lowercase SQL identifier: a letter or
// underscore, then letters, digits or underscores, at most 63 bytes.
func isIdentifier(n string) bool {
	if n == "" || len(n) > maxIdentifierLen || isDigit(rune(n[0])) {
		return false
	}

	for _, r := range n {
		if !isIdentifierRune(r) {
			return false
		}
	}

	return true
}

// isDigit reports an ASCII digit.
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// isIdentifierRune reports a lowercase letter, digit or underscore.
func isIdentifierRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || isDigit(r) || r == '_'
}

// runner is what a statement runs on: the database or a transaction.
type runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// on returns what a statement on ctx runs on: the transaction bound to it, else db.
func on(ctx context.Context, db *sql.DB) runner {
	if tx, ok := store.ClientFrom[*sql.Tx](ctx); ok {
		return tx
	}

	return db
}

// changed reports whether res, the outcome of the statement action names, touched
// exactly one row.
func changed(res sql.Result, err error, action string) (bool, error) {
	if err != nil {
		return false, apperr.Wrap(
			err, apperr.CodeUnavailable, "cas postgres store: "+action,
		)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return false, apperr.Wrap(
			err, apperr.CodeUnavailable, "cas postgres store: "+action,
		)
	}

	return rows == 1, nil
}

// New returns the node and ref stores over cfg's tables on pool.
// pool must not be nil.
// cfg must not be nil.
func New(pool *sql.DB, cfg *Config) (*Nodes, *Refs, error) {
	err := cfg.Validate()
	if err != nil {
		return nil, nil, err
	}

	nodes := &Nodes{
		db: pool,
		putSQL: "INSERT INTO " + cfg.nodesTable() +
			" (scope, hash, body) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING",
		getSQL: "SELECT body FROM " + cfg.nodesTable() +
			" WHERE scope = $1 AND hash = $2",
	}
	refs := &Refs{
		db: pool,
		refSQL: "SELECT hash FROM " + cfg.refsTable() +
			" WHERE scope = $1 AND name = $2",
		createSQL: "INSERT INTO " + cfg.refsTable() +
			" (scope, name, hash) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING",
		moveSQL: "UPDATE " + cfg.refsTable() +
			" SET hash = $4 WHERE scope = $1 AND name = $2 AND hash = $3",
	}

	return nodes, refs, nil
}

// Nodes is the PostgreSQL node store.
type Nodes struct {
	// db is the database statements run on outside a transaction.
	db *sql.DB

	// putSQL inserts a node unless its key exists.
	putSQL string

	// getSQL reads a node's body.
	getSQL string
}

// Put stores node once; see interfaces.NodeStore. A node with no scope or a zero hash
// is CodeInvalidInput.
func (s *Nodes) Put(ctx context.Context, node types.Node) (types.PutResult, error) {
	if node.Key.Scope == "" || node.Key.Hash.IsZero() {
		return types.PutResult{}, apperr.InvalidInput(
			"cas postgres store: a node needs a scope and a hash",
		)
	}

	res, err := on(ctx, s.db).ExecContext(
		ctx, s.putSQL, node.Key.Scope, node.Key.Hash[:], node.Body,
	)

	inserted, err := changed(res, err, "put node")
	if err != nil || inserted {
		return types.PutResult{Stored: inserted, Conflict: false}, err
	}

	stored, err := s.Get(ctx, node.Key)
	if err != nil {
		return types.PutResult{}, err
	}

	same := sameValue(stored.Body, node.Body)

	return types.PutResult{Stored: false, Conflict: !same}, nil
}

// Get returns the node stored under key; a missing one is CodeNotFound.
func (s *Nodes) Get(ctx context.Context, key types.NodeKey) (types.Node, error) {
	var body []byte

	row := on(ctx, s.db).QueryRowContext(ctx, s.getSQL, key.Scope, key.Hash[:])
	err := row.Scan(&body)

	if apperr.StdIs(err, sql.ErrNoRows) {
		return types.Node{}, apperr.New(
			apperr.CodeNotFound, "cas postgres store: no node",
		)
	}

	if err != nil {
		return types.Node{}, apperr.Wrap(
			err, apperr.CodeUnavailable, "cas postgres store: get node",
		)
	}

	return types.Node{Body: body, Key: key}, nil
}

// sameValue reports whether stored and incoming are the same JSON value (equal
// bytes, or equal canonical forms); bytes that are not JSON compare by content.
func sameValue(stored, incoming []byte) bool {
	if bytes.Equal(stored, incoming) {
		return true
	}

	left, errLeft := cas.Canonical(stored)
	right, errRight := cas.Canonical(incoming)

	return errLeft == nil && errRight == nil && bytes.Equal(left, right)
}

// Refs is the PostgreSQL ref store.
type Refs struct {
	// db is the database statements run on outside a transaction.
	db *sql.DB

	// refSQL reads a ref's hash.
	refSQL string

	// createSQL inserts a ref unless it exists.
	createSQL string

	// moveSQL points a ref at a new hash when it names the expected one.
	moveSQL string
}

// Ref returns the hash the ref names; a missing ref is CodeNotFound.
func (s *Refs) Ref(ctx context.Context, key types.RefKey) (types.Hash, error) {
	var raw []byte

	err := on(ctx, s.db).QueryRowContext(ctx, s.refSQL, key.Scope, key.Name).Scan(&raw)
	if apperr.StdIs(err, sql.ErrNoRows) {
		return types.Hash{}, apperr.New(apperr.CodeNotFound, "cas postgres store: no ref")
	}

	if err != nil {
		return types.Hash{}, apperr.Wrap(
			err, apperr.CodeUnavailable, "cas postgres store: get ref",
		)
	}

	var hash types.Hash

	if copy(hash[:], raw) != len(hash) || len(raw) != len(hash) {
		return types.Hash{}, apperr.New(
			apperr.CodeInternal, "cas postgres store: bad hash",
		)
	}

	return hash, nil
}

// Move points the ref at move.To by compare-and-swap; see interfaces.RefStore.
func (s *Refs) Move(ctx context.Context, move *types.RefMove) (bool, error) {
	err := move.Validate()
	if err != nil {
		return false, err
	}

	run := on(ctx, s.db)

	if move.From.IsZero() {
		res, execErr := run.ExecContext(
			ctx, s.createSQL, move.Key.Scope, move.Key.Name, move.To[:],
		)

		return changed(res, execErr, "create ref")
	}

	res, err := run.ExecContext(
		ctx, s.moveSQL, move.Key.Scope, move.Key.Name, move.From[:], move.To[:],
	)

	return changed(res, err, "move ref")
}

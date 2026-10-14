package cassandra

import (
	"context"
	"slices"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// statusCQL reads one table's creation status from the Amazon Keyspaces schema.
const statusCQL = "SELECT status FROM system_schema_mcs.tables" +
	" WHERE keyspace_name = ? AND table_name = ?"

// statusActive is the status of a Keyspaces table that is ready for use.
const statusActive = "ACTIVE"

// ensureSchema creates every declared table, in table-name order, within
// SchemaTimeout; with AwaitActive it then waits for each one to be ACTIVE. A
// table still not ACTIVE when the timeout passes is CodeUnavailable.
func (s *Store) ensureSchema(ctx context.Context, session interfaces.CQLSession) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.SchemaTimeout)
	defer cancel()
	type table struct{ name, ddl string }
	var tables []table
	for cube, grains := range s.cfg.Cubes {
		for _, grain := range grains {
			ddl, err := SchemaCQL(s.cfg.Keyspace, cube, grain)
			if err != nil {
				return err
			}
			tables = append(tables, table{name: TableName(cube, grain), ddl: ddl})
		}
	}
	slices.SortFunc(
		tables,
		func(a, b table) int { return strings.Compare(a.name, b.name) },
	)
	for _, t := range tables {
		if err := session.Query(t.ddl).WithContext(ctx).Exec(); err != nil {
			return err
		}
	}
	if !s.cfg.AwaitActive {
		return nil
	}
	for _, t := range tables {
		if err := s.awaitActive(ctx, session, t.name); err != nil {
			return err
		}
	}
	return nil
}

// awaitActive polls table's status every SchemaPollInterval until it is ACTIVE.
// The context ending first is CodeUnavailable.
func (s *Store) awaitActive(
	ctx context.Context,
	session interfaces.CQLSession,
	table string,
) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return apperr.New(
				apperr.CodeUnavailable,
				"analytics: table "+table+" is not ACTIVE within the schema timeout",
			)
		case <-timer.C:
		}
		status, err := s.tableStatus(ctx, session, table)
		if err != nil {
			return err
		}
		if status == statusActive {
			return nil
		}
		timer.Reset(s.cfg.SchemaPollInterval)
	}
}

// tableStatus reads table's status ("" when the table is not listed yet).
func (s *Store) tableStatus(
	ctx context.Context,
	session interfaces.CQLSession,
	table string,
) (string, error) {
	iter := session.Query(statusCQL, s.cfg.Keyspace, table).
		WithContext(ctx).
		Idempotent(true).
		Iter()
	var status string
	iter.Scan(&status)
	if err := iter.Close(); err != nil {
		return "", err
	}
	return status, nil
}

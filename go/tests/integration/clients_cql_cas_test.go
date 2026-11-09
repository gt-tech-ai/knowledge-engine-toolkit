//go:build integration

// This file verifies the CQL driver's lightweight transactions against a real single-node
// Cassandra 5 (testcontainers): Paxos semantics exist only in a real cluster.

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	cassandradb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/cassandra"
	testsuite "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite"
)

// casKeyspace is the keyspace the suite bootstraps.
const casKeyspace = "cas_it"

// CQLCASSuite holds a real Cassandra with one key-value table.
type CQLCASSuite struct {
	*testsuite.CassandraIntegrationSuite

	session interfaces.CQLSession
}

// TestCQLCASSuite runs the lightweight-transaction tests.
//
// Why this test is important:
//   - A conditional write is how a Cassandra-backed store refuses a stale writer; if the
//     driver misread [applied], every refused write would look applied (or the reverse).
//
// What it tests:
//   - Suite entrypoint wires CQLCASSuite into the testify runner.
func TestCQLCASSuite(t *testing.T) {
	t.Parallel()

	suite.Run(
		t,
		&CQLCASSuite{CassandraIntegrationSuite: &testsuite.CassandraIntegrationSuite{}},
	)
}

// SetupSuite creates the keyspace and table and opens the session.
func (s *CQLCASSuite) SetupSuite() {
	s.CassandraIntegrationSuite.SetupSuite()

	ctx := context.Background()
	s.Require().NoError(s.Cassandra.Exec(ctx,
		"CREATE KEYSPACE IF NOT EXISTS "+casKeyspace+" WITH replication = {'class': "+
			"'SimpleStrategy', 'replication_factor': 1}"))
	s.Require().NoError(s.Cassandra.Exec(
		ctx,
		"CREATE TABLE IF NOT EXISTS "+casKeyspace+".kv (k text PRIMARY KEY, v int, w text)",
	))

	cfg := cql.DefaultConfig()

	cfg.Cassandra.Hosts, cfg.Cassandra.Port = []string{s.CassandraHost}, s.CassandraPort
	cfg.Cassandra.LocalDC = cassandradb.LocalDC
	cfg.Keyspace, cfg.Timeout = casKeyspace, seconds20*time.Second

	session, err := cql.NewFromConfig(ctx, &cfg, cql.Deps{})
	s.Require().NoError(err)

	s.session = session
}

// TearDownSuite closes the session, then stops the container.
func (s *CQLCASSuite) TearDownSuite() {
	if s.session != nil {
		s.session.Close()
	}

	s.CassandraIntegrationSuite.TearDownSuite()
}

// TestApplyCAS_ReportsWhetherTheConditionHeld tests ApplyCAS on inserts and updates.
//
// Why this test is important:
//   - A refused lightweight transaction returns the current row's columns; reading them
//     into fixed destinations breaks whenever the table changes, so ApplyCAS must report
//     the outcome whatever columns come back.
//
// What it tests:
//   - INSERT IF NOT EXISTS applies once, then is refused (the refusal returns every
//     column).
//   - UPDATE IF v = <current> applies; UPDATE IF v = <stale> is refused and leaves the
//     row.
func (s *CQLCASSuite) TestApplyCAS_ReportsWhetherTheConditionHeld() {
	ctx := context.Background()
	insert := "INSERT INTO kv (k, v, w) VALUES (?, ?, ?) IF NOT EXISTS"
	applied, err := s.session.Query(insert, "a", 1, "x").WithContext(ctx).ApplyCAS()
	s.Require().NoError(err)
	s.True(applied)

	applied, err = s.session.Query(insert, "a", 2, "y").WithContext(ctx).ApplyCAS()
	s.Require().NoError(err)
	s.False(applied)

	update := "UPDATE kv SET v = ? WHERE k = ? IF v = ?"

	applied, err = s.session.Query(update, 3, "a", 1).WithContext(ctx).ApplyCAS()
	s.Require().NoError(err)
	s.True(applied)

	applied, err = s.session.Query(update, 4, "a", 1).WithContext(ctx).ApplyCAS()
	s.Require().NoError(err)
	s.False(applied)

	var v int

	s.Require().True(s.session.Query("SELECT v FROM kv WHERE k = ?", "a").
		WithContext(ctx).Iter().Scan(&v))
	s.Equal(equalArg3, v)
}

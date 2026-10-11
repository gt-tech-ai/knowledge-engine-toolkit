// Package postgres provides a generic testcontainers-based PostgreSQL database
// for integration tests.
//
// It is deliberately schema-agnostic: it starts a real PostgreSQL container and
// exposes the DSN, but applies NO application schema (no ORM schema, no consumer
// tables). A caller that needs a schema provisions it over the DSN — e.g. River
// applies its own migration, and a consumer's schema fixture can wrap this to run
// its own migrations. Keeping this fixture generic is what lets the engine's
// client integration tests (River, etc.) run without any consumer schema.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	// PostgreSQL driver registration.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

const (
	// dbUser is the superuser the PostgreSQL testcontainer is provisioned with.
	dbUser = "test"
	// dbPassword is the password for dbUser.
	dbPassword = "test"
	// dbName is the database created inside the testcontainer.
	dbName = "testdb"
	// pgImage is the PostgreSQL container image the fixture starts.
	pgImage = "postgres:16"
)

// TestDatabase wraps a testcontainers PostgreSQL instance. It holds no
// application schema — callers migrate over GetDSN() as needed.
type TestDatabase struct {
	// container is the running PostgreSQL testcontainer.
	container testcontainers.Container
	// dsn is the connection string for the containerized database.
	dsn string
}

// NewTestDatabase starts a PostgreSQL container and returns a TestDatabase whose
// DSN points at an empty database (no schema applied).
func NewTestDatabase(ctx context.Context) (*TestDatabase, error) {
	req := testcontainers.ContainerRequest{
		Image:        pgImage,
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     dbUser,
			"POSTGRES_PASSWORD": dbPassword,
			"POSTGRES_DB":       dbName,
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(30 * time.Second),
	}

	container, err := testcontainers.GenericContainer(
		ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: req,
			Started:          true,
		},
	)
	if err != nil {
		// A failed start (e.g. a readiness timeout) can still leave a container behind;
		// TerminateContainer is nil-safe.
		_ = testcontainers.TerminateContainer(container)
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"failed to start PostgreSQL container",
		)
	}

	host, err := container.Host(ctx)
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on setup failure
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"failed to get container host",
		)
	}

	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on setup failure
		return nil, apperr.Wrap(err, apperr.CodeInternal, "failed to get mapped port")
	}

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		dbUser, dbPassword, host, port.Port(), dbName)

	return &TestDatabase{
		container: container,
		dsn:       dsn,
	}, nil
}

// GetDSN returns the PostgreSQL connection string for direct SQL access.
func (td *TestDatabase) GetDSN() string {
	return td.dsn
}

// OpenDB opens a *sql.DB over the pgx driver for the container's DSN. The caller
// owns the returned handle and must Close it.
func (td *TestDatabase) OpenDB() (*sql.DB, error) {
	db, err := sql.Open("pgx", td.dsn)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "open database")
	}
	return db, nil
}

// Close terminates the container.
func (td *TestDatabase) Close(ctx context.Context) {
	if td.container != nil {
		td.container.Terminate(ctx) //nolint:errcheck // best-effort cleanup on teardown
	}
}

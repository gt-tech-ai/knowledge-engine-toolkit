// Package river provides River-based job queue integration: Runtime (a
// pgxpool-backed River client that works jobs, with leader-elected periodic
// jobs), InsertClient (an enqueue-only producer), and Migrate (River's own
// schema). Client, Scheduler and EventPublisher are no-op stand-ins for the core
// job interfaces that never touch Postgres, and WorkerRegistry is an in-memory
// registry.
package river

// Config holds River-specific job queue configuration.
type Config struct {
	// DatabaseURL is the PostgreSQL connection string for the job queue. The no-op
	// Client does not read it.
	DatabaseURL string
}

package interfaces

import "database/sql"

// DatabasePool is a lifecycle-managed database connection pool: it is started and
// stopped by a lifecycle.Manager, health-probed by Kubernetes (Liveness/Readiness),
// and exposes the shared *sql.DB the ORM adapters and raw-SQL callers run over. The
// backend (PostgreSQL today) is selected by the database tier's Kind, so switching
// database engines is a config change, not a caller edit.
type DatabasePool interface {
	// Client supplies the Start/Stop lifecycle and Kubernetes health probes.
	Client

	// DB returns the underlying *sql.DB the ORM adapters and raw-SQL callers share.
	// It is nil until Start opens the pool.
	DB() *sql.DB
}

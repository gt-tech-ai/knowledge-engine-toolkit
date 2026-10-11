package infra

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// DatabaseConfig holds PostgreSQL connection configuration.
type DatabaseConfig struct {
	// Host is the PostgreSQL server hostname or IP address.
	Host string `mapstructure:"host" envalias:"DB_HOST"`

	// User is the PostgreSQL login role used to authenticate.
	User string `mapstructure:"user" envalias:"DB_USER"`

	// Password is the PostgreSQL login password for User.
	Password string `mapstructure:"password" envalias:"DB_PASSWORD"`

	// Database is the PostgreSQL database name to connect to.
	Database string `mapstructure:"database" envalias:"DB_DATABASE"`

	// SSLMode controls TLS negotiation (e.g. "disable", "require", "verify-full").
	SSLMode string `mapstructure:"sslmode" envalias:"DB_SSLMODE"`

	// ConnectionMaxLifetime is the maximum duration a connection may be reused before it
	// is closed.
	ConnectionMaxLifetime time.Duration `mapstructure:"connection_max_lifetime"`

	// ConnMaxIdleTime is the maximum duration a connection may sit idle before it
	// is closed and removed from the pool. Bounding idle time (in addition to the
	// MaxIdleConnections count) means a burst that briefly inflates the idle pool
	// releases those connections back to the server instead of holding them for
	// ConnectionMaxLifetime, so replicas don't pin the shared connection budget.
	ConnMaxIdleTime time.Duration `mapstructure:"conn_max_idle_time"`

	// Port is the PostgreSQL server port (default 5432).
	Port int `mapstructure:"port" envalias:"DB_PORT"`

	// MaxConnections is the maximum number of open connections in the pool. This
	// is the per-replica Ent budget; with the dedicated River pgxpool it must
	// satisfy replicaCount * (MaxConnections + river.MaxConns) <= server
	// max_connections (see go/clients/jobs/river).
	MaxConnections int `mapstructure:"max_connections"`

	// MaxIdleConnections is the maximum number of idle connections kept in the
	// pool. Keep it a meaningful fraction of MaxConnections (not a fixed small
	// number), so a high MaxConnections isn't paired with a 20:1 open:idle ratio
	// that reopens connections under steady load.
	//
	// PgBouncer note: if a future deployment fronts Postgres with a
	// transaction-pooling PgBouncer, the pgx driver must be switched to the simple
	// query protocol (DSN default_query_exec_mode=simple_protocol, or
	// statement_cache_capacity=0) because prepared-statement caching is unsafe
	// across a transaction pooler. The DSN does not set either option, so a
	// deployment that adds such a pooler must.
	MaxIdleConnections int `mapstructure:"max_idle_connections"`
}

// DefaultDatabaseConfig returns a DatabaseConfig with defaults matching Docker Compose
// local dev.
func DefaultDatabaseConfig() DatabaseConfig {
	return DatabaseConfig{
		Host:                  "localhost",
		User:                  "app",
		Password:              "dev_password",
		Database:              "knowledge_engine",
		SSLMode:               "disable",
		ConnectionMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime:       2 * time.Minute,
		Port:                  5432,
		MaxConnections:        25,
		MaxIdleConnections:    5,
	}
}

// DSN returns a PostgreSQL connection string in the format:
// postgres://user:password@host:port/database?sslmode=disable
// Every component is URL-escaped, so credentials containing reserved characters
// (@ : / ? # % [ ]) and IPv6 hosts — bare ("::1") or already bracketed ("[::1]") —
// produce a DSN that parses back to the same values.
func (c *DatabaseConfig) DSN() string {
	host := strings.TrimSuffix(strings.TrimPrefix(c.Host, "["), "]")
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password),
		Host:     net.JoinHostPort(host, strconv.Itoa(c.Port)),
		Path:     "/" + c.Database,
		RawQuery: url.Values{"sslmode": {c.SSLMode}}.Encode(),
	}
	return dsn.String()
}

// Validate returns an error if the configuration is invalid.
func (c *DatabaseConfig) Validate() error {
	if c.Host == "" {
		return apperr.InvalidInput("database.host is required")
	}
	return nil
}

// Package cassandra builds the gocql cluster configuration for a self-managed
// Apache Cassandra (local Compose, dev): a token-aware, DC-aware host policy,
// optional password auth and TLS. It never dials; the parent package creates the
// session.
package cassandra

import (
	"crypto/tls"
	"time"

	"github.com/gocql/gocql"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Config is the self-managed Cassandra connection configuration.
type Config struct {
	// LocalDC is the local datacenter the DC-aware policy prefers.
	LocalDC string
	// Keyspace is the keyspace sessions use.
	Keyspace string
	// Consistency is the default consistency level name (e.g. LOCAL_QUORUM).
	Consistency string
	// Username enables password auth when non-empty.
	Username string
	// Password is the password-auth secret.
	Password string
	// Hosts are the contact points.
	Hosts []string
	// Timeout bounds each request.
	Timeout time.Duration
	// ConnectTimeout bounds each connection attempt.
	ConnectTimeout time.Duration
	// Port is the native-protocol port.
	Port int
	// PageSize is the default page size of a query.
	PageSize int
	// NumConns is the number of connections per host.
	NumConns int
	// TLS enables TLS with host verification.
	TLS bool
}

// New returns the cluster configuration for cfg, without dialing; a zero port or
// timeout keeps the driver's default. An empty host list or an unknown
// consistency is CodeInvalidInput.
func New(cfg *Config) (*gocql.ClusterConfig, error) {
	if len(cfg.Hosts) == 0 {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"cassandra: at least one host is required",
		)
	}
	consistency, err := gocql.ParseConsistencyWrapper(cfg.Consistency)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"cassandra: unknown consistency "+cfg.Consistency,
		)
	}
	cluster := gocql.NewCluster(cfg.Hosts...)
	if cfg.Port > 0 {
		cluster.Port = cfg.Port
	}
	cluster.Keyspace = cfg.Keyspace
	cluster.Consistency = consistency
	if cfg.Timeout > 0 {
		cluster.Timeout = cfg.Timeout
	}
	if cfg.ConnectTimeout > 0 {
		cluster.ConnectTimeout = cfg.ConnectTimeout
	}
	cluster.PageSize = cfg.PageSize
	if cfg.NumConns > 0 {
		cluster.NumConns = cfg.NumConns
	}
	cluster.PoolConfig.HostSelectionPolicy = gocql.TokenAwareHostPolicy(
		gocql.DCAwareRoundRobinPolicy(cfg.LocalDC),
	)
	if cfg.Username != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: cfg.Username,
			Password: cfg.Password,
		}
	}
	if cfg.TLS {
		cluster.SslOpts = &gocql.SslOptions{
			Config:                 &tls.Config{MinVersion: tls.VersionTLS12},
			EnableHostVerification: true,
		}
	}
	return cluster, nil
}

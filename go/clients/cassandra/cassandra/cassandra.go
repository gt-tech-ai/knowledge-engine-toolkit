// Package cassandra builds the gocql cluster configuration for a self-managed
// Apache Cassandra (local Compose, dev): a token-aware, DC-aware host policy,
// optional password auth and TLS. It never dials; the parent package creates the
// session.
package cassandra

import (
	"crypto/tls"
	"net"
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
	// DisableHostDiscovery keeps the driver on the one configured contact point, which
	// must be an IP literal, instead of the node addresses the cluster advertises: it
	// skips the initial peer lookup and translates every address the cluster reports
	// later to the contact point and Port. It is for a cluster reached through a
	// proxy, tunnel or port mapping whose advertised addresses are unreachable.
	DisableHostDiscovery bool
	// ReconnectInterval is how often the driver retries hosts it marked down; zero
	// keeps the driver default.
	ReconnectInterval time.Duration
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
	if cfg.DisableHostDiscovery {
		if err := pinContactPoint(cluster, cfg.Hosts); err != nil {
			return nil, err
		}
	}
	if cfg.ReconnectInterval > 0 {
		cluster.ReconnectInterval = cfg.ReconnectInterval
	}
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

// pinContactPoint keeps cluster on its single IP-literal contact point: the initial
// peer lookup is skipped, and every address a ring refresh or topology event reports
// is translated back to the contact point, so a node advertising an address the
// client cannot route to stays reachable. Anything but one IP literal is
// CodeInvalidInput, since a translated address must be an IP.
func pinContactPoint(cluster *gocql.ClusterConfig, hosts []string) error {
	var ip net.IP
	if len(hosts) == 1 {
		ip = net.ParseIP(hosts[0])
	}
	if ip == nil {
		return apperr.New(
			apperr.CodeInvalidInput,
			"cassandra: host discovery can be disabled only with one IP-literal host",
		)
	}
	port := cluster.Port
	cluster.DisableInitialHostLookup = true
	cluster.AddressTranslator = gocql.AddressTranslatorFunc(
		func(net.IP, int) (net.IP, int) { return ip, port },
	)
	return nil
}

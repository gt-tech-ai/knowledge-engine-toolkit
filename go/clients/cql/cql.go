// Package cql builds a CQL (Cassandra-protocol) Session — self-managed Apache
// Cassandra (KindCassandra, local and dev) or Amazon Keyspaces (KindKeyspaces,
// staging) — behind the consumer-side Session seam, so the choice is a config
// change. NewFromConfig selects the backend by Kind and returns the
// interfaces.CQLSession contract; each kind is a subpackage, and both dial through the
// gocql adapter in driver/.
//
// Only the common CQL dialect both backends support is used by callers: no
// lightweight transactions, counters, materialized views, UDFs or ALLOW FILTERING,
// and logged batches only within one partition.
package cql

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql/cassandra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql/keyspaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

const (
	// KindCassandra connects to a self-managed Apache Cassandra (the default).
	KindCassandra client.Kind = "cassandra"
	// KindKeyspaces connects to Amazon Keyspaces (TLS + SigV4).
	KindKeyspaces client.Kind = "keyspaces"
)

// Config is the configuration for a CQL session: the settings shared by every
// backend, plus one sub-config per backend.
type Config struct {
	// Kind selects the backend; empty means KindCassandra.
	Kind client.Kind `mapstructure:"kind"`
	// Keyspace is the keyspace sessions use.
	Keyspace string `mapstructure:"keyspace"`
	// Consistency is the default consistency level name (LOCAL_QUORUM).
	Consistency string `mapstructure:"consistency"`
	// Keyspaces holds the Amazon Keyspaces settings (KindKeyspaces).
	Keyspaces KeyspacesConfig `mapstructure:"keyspaces"`
	// Cassandra holds the self-managed Cassandra settings (KindCassandra).
	Cassandra CassandraConfig `mapstructure:"cassandra"`
	// Timeout bounds each request.
	Timeout time.Duration `mapstructure:"timeout"`
	// ConnectTimeout bounds each connection attempt.
	ConnectTimeout time.Duration `mapstructure:"connect_timeout"`
	// PageSize is the default page size of a query.
	PageSize int `mapstructure:"page_size"`
	// NumConns is the number of connections per host.
	NumConns int `mapstructure:"num_conns"`
}

// CassandraConfig holds the self-managed Cassandra settings.
type CassandraConfig struct {
	// LocalDC is the datacenter the DC-aware host policy prefers.
	LocalDC string `mapstructure:"local_dc"`
	// Username enables password auth when non-empty.
	Username string `mapstructure:"username"`
	// Password is the password-auth secret.
	Password string `mapstructure:"password"`
	// Hosts are the contact points.
	Hosts []string `mapstructure:"hosts"`
	// Port is the native-protocol port.
	Port int `mapstructure:"port"`
	// TLS enables TLS with host verification.
	TLS bool `mapstructure:"tls"`
	// DisableHostDiscovery keeps the driver on the one configured contact point, which
	// must be an IP literal, instead of the node addresses the cluster advertises, for
	// a cluster reached through a proxy, tunnel or port mapping whose advertised
	// addresses are unreachable.
	DisableHostDiscovery bool `mapstructure:"disable_host_discovery"`
	// ReconnectInterval is how often the driver retries hosts it marked down; zero
	// keeps the driver default.
	ReconnectInterval time.Duration `mapstructure:"reconnect_interval"`
}

// KeyspacesConfig holds the Amazon Keyspaces settings. Credentials come from the
// AWS default chain (IRSA in-cluster).
type KeyspacesConfig struct {
	// Region is the AWS region of the Keyspaces endpoint.
	Region string `mapstructure:"region"`
	// CACertPath, when set, is a PEM CA bundle to trust instead of the system pool.
	CACertPath string `mapstructure:"ca_cert_path"`
	// Port is the TLS native-protocol port.
	Port int `mapstructure:"port"`
}

// DefaultConfig returns the defaults: the self-managed backend on localhost:9042,
// LOCAL_QUORUM, 5 s request and connect timeouts, 500-row pages, 2 connections
// per host, and Keyspaces port 9142.
func DefaultConfig() Config {
	return Config{
		Kind:           KindCassandra,
		Consistency:    "LOCAL_QUORUM",
		Timeout:        5 * time.Second,
		ConnectTimeout: 5 * time.Second,
		PageSize:       500,
		NumConns:       2,
		Cassandra:      CassandraConfig{Hosts: []string{"localhost"}, Port: 9042},
		Keyspaces:      KeyspacesConfig{Port: keyspaces.DefaultPort},
	}
}

// Deps carries a session's runtime collaborators; the backends need none today.
type Deps struct{}

// kinds maps each Kind to the backend that dials its Session.
func kinds() client.Kinds[Config, Deps, interfaces.CQLSession] {
	return client.Kinds[Config, Deps, interfaces.CQLSession]{
		Family:  "cql",
		Default: KindCassandra,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.CQLSession]{
			KindCassandra: newCassandra,
			KindKeyspaces: newKeyspaces,
		},
	}
}

// NewFromConfig builds the cluster for cfg.Kind and dials a Session. An unknown
// kind or an invalid backend config is CodeInvalidInput; a failed dial is
// CodeUnavailable.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.CQLSession, error) {
	return kinds().New(ctx, cfg.Kind, cfg, deps)
}

// newCassandra dials a self-managed Cassandra.
func newCassandra(_ context.Context, cfg Config, _ Deps) (interfaces.CQLSession, error) {
	return cassandra.New(&cassandra.Config{
		Hosts:          cfg.Cassandra.Hosts,
		LocalDC:        cfg.Cassandra.LocalDC,
		Keyspace:       cfg.Keyspace,
		Consistency:    cfg.Consistency,
		Username:       cfg.Cassandra.Username,
		Password:       cfg.Cassandra.Password,
		Timeout:        cfg.Timeout,
		ConnectTimeout: cfg.ConnectTimeout,
		Port:           cfg.Cassandra.Port,
		PageSize:       cfg.PageSize,
		NumConns:       cfg.NumConns,
		TLS:            cfg.Cassandra.TLS,

		DisableHostDiscovery: cfg.Cassandra.DisableHostDiscovery,
		ReconnectInterval:    cfg.Cassandra.ReconnectInterval,
	})
}

// newKeyspaces dials Amazon Keyspaces. The SigV4 credentials callback runs on each
// refresh, long after construction, and bounds itself, so no construction context
// reaches it.
func newKeyspaces(_ context.Context, cfg Config, _ Deps) (interfaces.CQLSession, error) {
	return keyspaces.New(&keyspaces.Config{ //nolint:contextcheck // see the doc comment
		Region:         cfg.Keyspaces.Region,
		Keyspace:       cfg.Keyspace,
		Consistency:    cfg.Consistency,
		CACertPath:     cfg.Keyspaces.CACertPath,
		Timeout:        cfg.Timeout,
		ConnectTimeout: cfg.ConnectTimeout,
		Port:           cfg.Keyspaces.Port,
		PageSize:       cfg.PageSize,
		NumConns:       cfg.NumConns,
	})
}

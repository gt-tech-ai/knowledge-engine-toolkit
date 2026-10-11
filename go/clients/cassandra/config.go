package cassandra

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Config is the configuration for a Cassandra-protocol session: the settings
// shared by every backend, plus one sub-config per backend.
type Config struct {
	// Keyspace is the keyspace sessions use.
	Keyspace string
	// Consistency is the default consistency level name (LOCAL_QUORUM).
	Consistency string
	// Keyspaces holds the Amazon Keyspaces settings (KindKeyspaces).
	Keyspaces KeyspacesConfig
	// Cassandra holds the self-managed Cassandra settings (KindCassandra).
	Cassandra CassandraConfig
	// Kind selects the backend.
	Kind Kind
	// Timeout bounds each request.
	Timeout time.Duration
	// ConnectTimeout bounds each connection attempt.
	ConnectTimeout time.Duration
	// PageSize is the default page size of a query.
	PageSize int
	// NumConns is the number of connections per host.
	NumConns int
}

// CassandraConfig holds the self-managed Cassandra settings.
type CassandraConfig struct {
	// LocalDC is the datacenter the DC-aware host policy prefers.
	LocalDC string
	// Username enables password auth when non-empty.
	Username string
	// Password is the password-auth secret.
	Password string
	// Hosts are the contact points.
	Hosts []string
	// Port is the native-protocol port.
	Port int
	// TLS enables TLS with host verification.
	TLS bool
}

// KeyspacesConfig holds the Amazon Keyspaces settings. Credentials come from the
// AWS default chain (IRSA in-cluster).
type KeyspacesConfig struct {
	// Region is the AWS region of the Keyspaces endpoint.
	Region string
	// CACertPath, when set, is a PEM CA bundle to trust instead of the system pool.
	CACertPath string
	// Port is the TLS native-protocol port.
	Port int
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
		Keyspaces:      KeyspacesConfig{Port: 9142},
	}
}

// WithKeyspace sets the keyspace.
func WithKeyspace(keyspace string) options.Option[Config] {
	return func(c *Config) { c.Keyspace = keyspace }
}

// WithConsistency sets the default consistency level name.
func WithConsistency(consistency string) options.Option[Config] {
	return func(c *Config) { c.Consistency = consistency }
}

// WithTimeout sets the per-request timeout.
func WithTimeout(d time.Duration) options.Option[Config] {
	return func(c *Config) { c.Timeout = d }
}

// WithPageSize sets the default page size.
func WithPageSize(n int) options.Option[Config] {
	return func(c *Config) { c.PageSize = n }
}

// WithHosts sets the self-managed contact points and port.
func WithHosts(port int, hosts ...string) options.Option[Config] {
	return func(c *Config) {
		c.Cassandra.Hosts = hosts
		c.Cassandra.Port = port
	}
}

// WithLocalDC sets the self-managed local datacenter.
func WithLocalDC(dc string) options.Option[Config] {
	return func(c *Config) { c.Cassandra.LocalDC = dc }
}

// WithKeyspacesRegion sets the Amazon Keyspaces region.
func WithKeyspacesRegion(region string) options.Option[Config] {
	return func(c *Config) { c.Keyspaces.Region = region }
}

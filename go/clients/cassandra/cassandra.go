// Package cassandra builds a Cassandra-protocol Session — self-managed Apache
// Cassandra (KindCassandra, local and dev) or Amazon Keyspaces (KindKeyspaces,
// staging) — behind the consumer-side Session seam, so the choice is a config
// change. Use New() or NewFromConfig(); each backend lives in its own subpackage
// and only builds the gocql cluster configuration.
//
// Only the common CQL dialect both backends support is used by callers: no
// lightweight transactions, counters, materialized views, UDFs or ALLOW FILTERING,
// and logged batches only within one partition.
package cassandra

import (
	"fmt"
	"time"

	"github.com/gocql/gocql"

	cassandrabackend "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cassandra/cassandra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cassandra/keyspaces"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Kind specifies which Cassandra-protocol backend to connect to.
type Kind int

const (
	// KindCassandra connects to a self-managed Apache Cassandra.
	KindCassandra Kind = iota
	// KindKeyspaces connects to Amazon Keyspaces (TLS + SigV4).
	KindKeyspaces
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindCassandra:
		return "cassandra"
	case KindKeyspaces:
		return "keyspaces"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// speculativeDelay is when a speculative retry of an idempotent read is sent to
// another replica (self-managed Cassandra only; Keyspaces bills each request).
const speculativeDelay = 50 * time.Millisecond

// New creates a Session of the given kind with optional functional options.
func New(kind Kind, opts ...options.Option[Config]) (Session, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(&cfg)
}

// NewFromConfig builds the cluster for cfg.Kind and dials a Session. An unknown
// kind or an invalid backend config is CodeInvalidInput; a failed dial is
// CodeUnavailable.
func NewFromConfig(cfg *Config) (Session, error) {
	cluster, speculative, err := clusterFor(cfg)
	if err != nil {
		return nil, err
	}
	s, err := cluster.CreateSession()
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"cassandra: create session",
		)
	}
	return &gocqlSession{session: s, speculative: speculative}, nil
}

// clusterFor builds the backend's cluster configuration and the speculative
// execution policy its idempotent reads use (nil for none).
func clusterFor(
	cfg *Config,
) (*gocql.ClusterConfig, gocql.SpeculativeExecutionPolicy, error) {
	switch cfg.Kind {
	case KindCassandra:
		cluster, err := cassandrabackend.New(&cassandrabackend.Config{
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
		return cluster, &gocql.SimpleSpeculativeExecution{
			NumAttempts:  1,
			TimeoutDelay: speculativeDelay,
		}, err
	case KindKeyspaces:
		cluster, err := keyspaces.New(&keyspaces.Config{
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
		return cluster, nil, err
	default:
		return nil, nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown cassandra kind: %v", cfg.Kind),
		)
	}
}

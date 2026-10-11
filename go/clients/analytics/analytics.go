// Package analytics builds the AnalyticsStore: the zero-infrastructure stub
// (the default) or the Cassandra-protocol store (Apache Cassandra locally,
// Amazon Keyspaces in staging, chosen by the nested Cassandra config). Use New()
// or NewFromConfig(); decorate the result with the decorators subpackage.
package analytics

import (
	"fmt"

	analyticscassandra "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics/cassandra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics/stub"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cassandra"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Kind specifies which analytics store implementation to use.
type Kind int

const (
	// KindStub accepts writes and answers empty results (the default).
	KindStub Kind = iota
	// KindCassandra stores partial rows in Cassandra or Amazon Keyspaces.
	KindCassandra
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindStub:
		return "stub"
	case KindCassandra:
		return "cassandra"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// New creates an AnalyticsStore of the given kind with optional functional options.
func New(kind Kind, opts ...options.Option[Config]) (interfaces.AnalyticsStore, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(&cfg)
}

// NewFromConfig creates an AnalyticsStore from cfg without I/O. The cassandra
// kind dials its session from cfg.Cassandra at Start (and, with EnsureSchema,
// creates its tables there, awaiting ACTIVE on Keyspaces); an unknown kind is
// CodeInvalidInput.
func NewFromConfig(cfg *Config) (interfaces.AnalyticsStore, error) {
	switch cfg.Kind {
	case KindStub:
		return stub.New(), nil
	case KindCassandra:
		dial := cfg.SessionFactory
		if dial == nil {
			dial = cassandra.NewFromConfig
		}
		sessionCfg := cfg.Cassandra
		store, err := analyticscassandra.NewLazy(
			func() (cassandra.Session, error) { return dial(&sessionCfg) },
			analyticscassandra.Config{
				Keyspace:             cfg.Cassandra.Keyspace,
				Cubes:                cfg.Cubes,
				BucketWidth:          cfg.BucketWidth,
				TTL:                  cfg.TTL,
				PageSize:             cfg.Cassandra.PageSize,
				MaxConcurrentBuckets: cfg.MaxConcurrentBuckets,
				EnsureSchema:         cfg.EnsureSchema,
				AwaitActive:          cfg.Cassandra.Kind == cassandra.KindKeyspaces,
			},
		)
		if err != nil {
			return nil, err
		}
		return store, nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown analytics kind: %v", cfg.Kind),
		)
	}
}

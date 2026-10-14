// Package analytics builds the AnalyticsStore: the zero-infrastructure stub (the
// default) or the CQL store (Apache Cassandra locally, Amazon Keyspaces in staging,
// chosen by the nested CQL config). NewFromConfig selects the backend by Kind and
// runs it through the client tier (Decorate).
package analytics

import (
	"context"
	"time"

	analyticscassandra "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics/cassandra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/analytics/stub"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cql"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindStub accepts writes and answers empty results (the default).
	KindStub = client.KindStub
	// KindCassandra stores partial rows over a CQL session (Cassandra or Keyspaces).
	KindCassandra client.Kind = "cassandra"
)

// Config is the analytics store configuration.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `mapstructure:"kind"`
	// BucketWidth is the partition bucket of each grain, as a coarser grain (a
	// grain's groups never span buckets): minute→hour, hour→day, day→month, month→month.
	BucketWidth map[types.Grain]types.Grain `mapstructure:"bucket_width"`
	// Cubes declares each cube and the grains it is rolled up at (one table each).
	Cubes map[string][]types.Grain `mapstructure:"cubes"`
	// TTL is how long a row of each grain is kept (0 or absent = forever).
	TTL map[types.Grain]time.Duration `mapstructure:"ttl"`
	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("analytics").
	Name string `yaml:"name" mapstructure:"name"`
	// CQL is the session config; its Kind picks Cassandra or Keyspaces.
	CQL cql.Config `mapstructure:"cql"`
	// MaxConcurrentBuckets bounds how many buckets a query reads at once.
	MaxConcurrentBuckets int `mapstructure:"max_concurrent_buckets"`
	// EnsureSchema makes the cassandra kind create every declared table at Start
	// and, on Amazon Keyspaces (CQL.Kind), wait until each one is ACTIVE.
	EnsureSchema bool `mapstructure:"ensure_schema"`
}

// DefaultConfig returns the stub kind with the default bucket widths, 8
// concurrent buckets and the default CQL session config.
func DefaultConfig() Config {
	return Config{
		Kind: KindStub,
		CQL:  cql.DefaultConfig(),
		BucketWidth: map[types.Grain]types.Grain{
			types.GrainMinute: types.GrainHour,
			types.GrainHour:   types.GrainDay,
			types.GrainDay:    types.GrainMonth,
			types.GrainMonth:  types.GrainMonth,
		},
		Cubes:                map[string][]types.Grain{},
		TTL:                  map[types.Grain]time.Duration{},
		MaxConcurrentBuckets: 8,
	}
}

// Dial opens the CQL session a cassandra-kind store runs on.
type Dial func(ctx context.Context, cfg cql.Config) (interfaces.CQLSession, error)

// Deps carries the store's runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config (decoration.clients)
	// and supplies its observability.
	decorate.Decorations
	// Dial opens the CQL session at Start; nil dials with cql.NewFromConfig.
	Dial Dial
}

// kinds maps each Kind to its AnalyticsStore backend.
func kinds() client.Kinds[Config, Deps, interfaces.AnalyticsStore] {
	return client.Kinds[Config, Deps, interfaces.AnalyticsStore]{
		Family:   "analytics",
		Default:  KindStub,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.AnalyticsStore]{
			KindStub: func(context.Context, Config, Deps) (interfaces.AnalyticsStore, error) {
				return stub.New(), nil
			},
			KindCassandra: newCassandra,
		},
	}
}

// NewFromConfig builds the AnalyticsStore cfg.Kind names, decorated by the client
// tier, without I/O: the cassandra kind dials its session at Start (and, with
// EnsureSchema, creates its tables there, awaiting ACTIVE on Keyspaces). An unknown
// kind is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.AnalyticsStore, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}

// newCassandra builds the CQL store, dialing lazily at Start.
func newCassandra(
	_ context.Context,
	cfg Config,
	deps Deps,
) (interfaces.AnalyticsStore, error) {
	dial := deps.Dial
	if dial == nil {
		dial = func(ctx context.Context, c cql.Config) (interfaces.CQLSession, error) {
			return cql.NewFromConfig(ctx, c, cql.Deps{})
		}
	}
	session := cfg.CQL
	return analyticscassandra.NewLazy(
		func(ctx context.Context) (interfaces.CQLSession, error) { return dial(ctx, session) },
		analyticscassandra.Config{
			Keyspace:             cfg.CQL.Keyspace,
			Cubes:                cfg.Cubes,
			BucketWidth:          cfg.BucketWidth,
			TTL:                  cfg.TTL,
			PageSize:             cfg.CQL.PageSize,
			MaxConcurrentBuckets: cfg.MaxConcurrentBuckets,
			EnsureSchema:         cfg.EnsureSchema,
			AwaitActive:          cfg.CQL.Kind == cql.KindKeyspaces,
		},
	)
}

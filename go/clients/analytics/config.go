package analytics

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cassandra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// SessionFactory dials a Cassandra-protocol session from its config; the default
// is cassandra.NewFromConfig (injectable so the wiring is unit-testable).
type SessionFactory func(cfg *cassandra.Config) (cassandra.Session, error)

// Config is the analytics store configuration.
type Config struct {
	// SessionFactory dials the session for KindCassandra (nil = cassandra.NewFromConfig).
	SessionFactory SessionFactory
	// BucketWidth is the partition bucket of each grain, as a coarser grain (a
	// grain's groups never span buckets): minute→hour, hour→day, day→month, month→month.
	BucketWidth map[types.Grain]types.Grain
	// Cubes declares each cube and the grains it is rolled up at (one table each).
	Cubes map[string][]types.Grain
	// TTL is how long a row of each grain is kept (0 or absent = forever).
	TTL map[types.Grain]time.Duration
	// Cassandra is the session config; its Kind picks Cassandra or Keyspaces.
	Cassandra cassandra.Config
	// Kind selects the backend.
	Kind Kind
	// MaxConcurrentBuckets bounds how many buckets a query reads at once.
	MaxConcurrentBuckets int
	// EnsureSchema makes the cassandra kind create every declared table at Start
	// and, on Amazon Keyspaces (Cassandra.Kind), wait until each one is ACTIVE.
	EnsureSchema bool
}

// DefaultConfig returns the stub kind with the default bucket widths, 8
// concurrent buckets and the default Cassandra session config.
func DefaultConfig() Config {
	return Config{
		Kind:      KindStub,
		Cassandra: cassandra.DefaultConfig(),
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

// WithCassandra sets the nested session kind and applies session options.
func WithCassandra(
	kind cassandra.Kind,
	opts ...options.Option[cassandra.Config],
) options.Option[Config] {
	return func(c *Config) {
		c.Cassandra.Kind = kind
		options.ApplyOptions(&c.Cassandra, opts...)
	}
}

// WithCube declares a cube and the grains it is rolled up at.
func WithCube(name string, grains ...types.Grain) options.Option[Config] {
	return func(c *Config) {
		if c.Cubes == nil {
			c.Cubes = map[string][]types.Grain{}
		}
		c.Cubes[name] = grains
	}
}

// WithTTL keeps rows of grain for d.
func WithTTL(grain types.Grain, d time.Duration) options.Option[Config] {
	return func(c *Config) {
		if c.TTL == nil {
			c.TTL = map[types.Grain]time.Duration{}
		}
		c.TTL[grain] = d
	}
}

// WithMaxConcurrentBuckets bounds a query's bucket fan-out.
func WithMaxConcurrentBuckets(n int) options.Option[Config] {
	return func(c *Config) { c.MaxConcurrentBuckets = n }
}

// WithEnsureSchema makes Start create the declared tables (see Config.EnsureSchema).
func WithEnsureSchema() options.Option[Config] {
	return func(c *Config) { c.EnsureSchema = true }
}

// WithSessionFactory overrides how the Cassandra session is dialed.
func WithSessionFactory(f SessionFactory) options.Option[Config] {
	return func(c *Config) { c.SessionFactory = f }
}

// Package cassandra is the Cassandra-protocol AnalyticsStore (Apache Cassandra or
// Amazon Keyspaces behind clients/cassandra.Session).
//
// Model: one table per declared cube and grain (TableName), partition
// ((org_id, cube, bucket)) with bucket = ts truncated to the grain's bucket width,
// clustering (ts, dims_key, idempotency_key). Each fact writes one partial row
// per grain — partials map<text, blob> holding a chartspec.EncodePartial per measure —
// so a redelivered fact overwrites its own row with the same bytes: writes are
// idempotent with no lightweight transaction, no counter and no read-before-write.
// Aggregate reads the buckets of the time range a page at a time with bounded
// prefetch and applies the filter in Go (see residual.go). CQL stays in the
// dialect both backends share.
package cassandra

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cassandra"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/chartspec"
)

// Compile-time interface assertions.
var (
	_ interfaces.AnalyticsStore     = (*Store)(nil)
	_ interfaces.AnalyticsCompactor = (*Store)(nil)
)

// identifier is the shape a cube name must have to become part of a table name.
var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)

// Config configures the store.
type Config struct {
	// Cubes declares each cube and the grains it is rolled up at.
	Cubes map[string][]types.Grain
	// BucketWidth is each grain's partition bucket, as a coarser grain.
	BucketWidth map[types.Grain]types.Grain
	// TTL is how long rows of each grain are kept (0 or absent = forever).
	TTL map[types.Grain]time.Duration
	// Keyspace qualifies the table names in SchemaCQL.
	Keyspace string
	// PageSize is the rows per read page (0 = 500).
	PageSize int
	// MaxConcurrentBuckets bounds bucket fan-out on reads and writes (0 = 8).
	MaxConcurrentBuckets int
	// SchemaPollInterval is how often AwaitActive re-reads a table's status (0 = 1 s).
	SchemaPollInterval time.Duration
	// SchemaTimeout bounds EnsureSchema, including the wait for ACTIVE (0 = 2 min).
	SchemaTimeout time.Duration
	// EnsureSchema makes Start create every declared table (CREATE TABLE IF NOT
	// EXISTS), so a fresh keyspace serves with no separate DDL step.
	EnsureSchema bool
	// AwaitActive makes EnsureSchema wait until each table's status in
	// system_schema_mcs.tables is ACTIVE: Amazon Keyspaces creates tables
	// asynchronously, and a table is unusable until then.
	AwaitActive bool
}

// Store is the Cassandra-protocol AnalyticsStore.
type Store struct {
	// session is the Cassandra-protocol session (its lifecycle is the store's);
	// nil until Start when the store dials lazily.
	session cassandra.Session
	// dial opens the session at Start (nil when New was given a session).
	dial func() (cassandra.Session, error)
	// cfg is the validated configuration.
	cfg Config
	// mu guards session and schemaReady.
	mu sync.RWMutex
	// schemaReady is set once EnsureSchema has succeeded, so a repeated Start runs
	// no DDL.
	schemaReady bool
}

// New returns a store over session. A cube name that is not a lower-case
// identifier, or a declared grain that is unknown or lacks a known bucket width,
// is CodeInvalidInput.
func New(session cassandra.Session, cfg Config) (*Store, error) {
	s, err := newStore(cfg)
	if err != nil {
		return nil, err
	}
	s.session = session
	return s, nil
}

// NewLazy returns a store that dials its session with dial at Start, so
// construction does no I/O; an operation before Start is CodeUnavailable. The
// config is validated as by New.
func NewLazy(dial func() (cassandra.Session, error), cfg Config) (*Store, error) {
	s, err := newStore(cfg)
	if err != nil {
		return nil, err
	}
	s.dial = dial
	return s, nil
}

// newStore validates cfg and applies its defaults.
func newStore(cfg Config) (*Store, error) {
	if cfg.PageSize <= 0 {
		cfg.PageSize = 500
	}
	if cfg.MaxConcurrentBuckets <= 0 {
		cfg.MaxConcurrentBuckets = 8
	}
	if cfg.SchemaPollInterval <= 0 {
		cfg.SchemaPollInterval = time.Second
	}
	if cfg.SchemaTimeout <= 0 {
		cfg.SchemaTimeout = 2 * time.Minute
	}
	for cube, grains := range cfg.Cubes {
		if !identifier.MatchString(cube) {
			return nil, apperr.New(
				apperr.CodeInvalidInput,
				"analytics: invalid cube name "+cube,
			)
		}
		for _, g := range grains {
			width, ok := cfg.BucketWidth[g]
			if !ok {
				return nil, apperr.New(
					apperr.CodeInvalidInput,
					"analytics: no bucket width for grain "+string(g),
				)
			}
			if !chartspec.ValidGrain(g) || !chartspec.ValidGrain(width) {
				return nil, apperr.New(
					apperr.CodeInvalidInput,
					"analytics: unknown grain or bucket width for "+string(g),
				)
			}
		}
	}
	return &Store{cfg: cfg}, nil
}

// TableName returns the table holding cube's rows at grain.
func TableName(cube string, grain types.Grain) string {
	return cube + "_" + string(grain)
}

// SchemaCQL returns the CREATE TABLE statement of cube at grain in keyspace.
func SchemaCQL(keyspace, cube string, grain types.Grain) (string, error) {
	if !identifier.MatchString(cube) || !identifier.MatchString(keyspace) {
		return "", apperr.New(
			apperr.CodeInvalidInput,
			"analytics: invalid keyspace or cube name",
		)
	}
	return "CREATE TABLE IF NOT EXISTS " + keyspace + "." + TableName(cube, grain) + ` (
  org_id text, cube text, bucket timestamp, ts timestamp, dims_key text,
  idempotency_key text,
  dims map<text, text>, partials map<text, blob>,
  PRIMARY KEY ((org_id, cube, bucket), ts, dims_key, idempotency_key))`, nil
}

// Start dials the session of a lazy store (once; a store built by New, or
// already started, keeps its session) and, when EnsureSchema is set, creates the
// declared tables (see ensureSchema) until that has succeeded once. A dial
// failure is returned as coded by dial; a schema failure closes a session this
// Start dialed, so a retried Start dials afresh.
func (s *Store) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dialed := false
	if s.session == nil && s.dial != nil {
		session, err := s.dial()
		if err != nil {
			return err
		}
		s.session, dialed = session, true
	}
	if !s.cfg.EnsureSchema || s.schemaReady || s.session == nil {
		return nil
	}
	if err := s.ensureSchema(ctx, s.session); err != nil {
		if dialed {
			s.session.Close()
			s.session = nil
		}
		return err
	}
	s.schemaReady = true
	return nil
}

// Stop closes the session, if one was opened.
func (s *Store) Stop(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		s.session.Close()
		s.session = nil
	}
	return nil
}

// readinessQuery is the cheapest statement every Cassandra-compatible endpoint
// (Cassandra, Keyspaces, ScyllaDB) answers: the coordinator's own system row.
const readinessQuery = "SELECT release_version FROM system.local"

// Liveness is a self-check with no I/O: the store holds no state that can wedge.
func (*Store) Liveness(context.Context) error { return nil }

// Readiness runs readinessQuery on the open session under ctx. Before Start (or
// after Stop) it is CodeUnavailable; a failed query is CodeUnavailable too, so a
// probe never surfaces a driver error's hosts or credentials as anything else.
func (s *Store) Readiness(ctx context.Context) error {
	session, err := s.sess()
	if err != nil {
		return err
	}
	err = session.Query(readinessQuery).WithContext(ctx).Idempotent(true).Exec()
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "analytics: store is not ready")
	}
	return nil
}

// sess returns the open session; before Start (or after Stop) it is CodeUnavailable.
func (s *Store) sess() (cassandra.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.session == nil {
		return nil, apperr.New(apperr.CodeUnavailable, "analytics: store is not started")
	}
	return s.session, nil
}

// DimsKey renders dims canonically (sorted, URL-escaped key=value pairs): the
// clustering key that groups rows with identical dimensions.
func DimsKey(dims map[string]string) string {
	values := url.Values{}
	for k, v := range dims {
		values.Set(k, v)
	}
	return values.Encode()
}

// Write upserts one partial row per fact per declared grain of its cube, with
// bounded concurrency. A fact for an undeclared cube, without an org or with an
// empty idempotency key (reserved for compacted rows), with a schema version
// other than FactSchemaVersion (0, an in-process fact, is accepted), or with a
// non-finite measure, is CodeInvalidInput.
func (s *Store) Write(ctx context.Context, facts []types.Fact) error {
	type job struct {
		partials map[string][]byte
		grain    types.Grain
		fact     types.Fact
	}
	session, err := s.sess()
	if err != nil {
		return err
	}
	var jobs []job
	for _, fact := range facts {
		grains, ok := s.cfg.Cubes[fact.Cube]
		if !ok {
			return apperr.New(
				apperr.CodeInvalidInput,
				"analytics: undeclared cube "+fact.Cube,
			)
		}
		if fact.OrgID == "" || fact.IdempotencyKey == "" {
			return apperr.New(
				apperr.CodeInvalidInput,
				"analytics: a fact needs an org_id and an idempotency_key",
			)
		}
		if fact.Schema != 0 && fact.Schema != types.FactSchemaVersion {
			return apperr.New(
				apperr.CodeInvalidInput,
				"analytics: unknown fact schema version",
			)
		}
		partials := make(map[string][]byte, len(fact.Measures))
		for name, v := range fact.Measures {
			p, err := chartspec.NewPartial(v)
			if err != nil {
				return err
			}
			partials[name] = chartspec.EncodePartial(p)
		}
		for _, grain := range grains {
			jobs = append(jobs, job{partials: partials, fact: fact, grain: grain})
		}
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.cfg.MaxConcurrentBuckets)
	for _, j := range jobs {
		g.Go(func() error { return s.insert(gctx, session, j.fact, j.grain, j.partials) })
	}
	return g.Wait()
}

// insert writes one fact's partial row into grain's table.
func (s *Store) insert(
	ctx context.Context,
	session cassandra.Session,
	fact types.Fact,
	grain types.Grain,
	partials map[string][]byte,
) error {
	stmt := "INSERT INTO " + TableName(fact.Cube, grain) +
		" (org_id, cube, bucket, ts, dims_key, idempotency_key, dims, partials)" +
		" VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
	args := []any{
		fact.OrgID,
		fact.Cube,
		chartspec.Truncate(fact.TS, s.cfg.BucketWidth[grain]),
		chartspec.Truncate(fact.TS, grain),
		DimsKey(fact.Dims),
		fact.IdempotencyKey,
		fact.Dims,
		partials,
	}
	if ttl := s.cfg.TTL[grain]; ttl > 0 {
		stmt += " USING TTL ?"
		args = append(args, int(ttl/time.Second))
	}
	return session.Query(stmt, args...).WithContext(ctx).Idempotent(true).Exec()
}

// Aggregate opens a page-at-a-time stream over the buckets of q.TimeRange. The
// table is q.Grain's (the cube's coarsest grain when unset). An undeclared cube or
// grain, a missing org, an open-ended time range, or a resume token issued for a
// different query is CodeInvalidInput; a store not yet started is CodeUnavailable.
func (s *Store) Aggregate(
	ctx context.Context,
	q types.AggregateQuery,
) (interfaces.RowStream, error) {
	grains, ok := s.cfg.Cubes[q.Cube]
	if !ok {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"analytics: undeclared cube "+q.Cube,
		)
	}
	grain := q.Grain
	if grain == "" && len(grains) > 0 {
		grain = coarsest(grains)
	}
	if !slices.Contains(grains, grain) {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"analytics: cube "+q.Cube+" has no grain "+string(grain),
		)
	}
	if q.OrgID == "" || q.TimeRange.From.IsZero() || q.TimeRange.To.IsZero() ||
		!q.TimeRange.From.Before(q.TimeRange.To) {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"analytics: a query needs an org_id and a bounded time range",
		)
	}
	width := s.cfg.BucketWidth[grain]
	lo, hi := pushdown(q.Filter, q.TimeRange.From.UTC(), q.TimeRange.To.UTC())
	var buckets []time.Time
	for b := chartspec.Truncate(lo, width); b.Before(hi); b = chartspec.Next(b, width) {
		buckets = append(buckets, b)
	}
	fingerprint, err := queryFingerprint(q)
	if err != nil {
		return nil, err
	}
	resume, err := decodeToken(q.ResumeToken, fingerprint)
	if err != nil {
		return nil, err
	}
	session, err := s.sess()
	if err != nil {
		return nil, err
	}
	plan := &readPlan{
		session:     session,
		fingerprint: fingerprint,
		table:       TableName(q.Cube, grain),
		org:         q.OrgID,
		cube:        q.Cube,
		lo:          lo,
		hi:          hi,
		pageSize:    s.cfg.PageSize,
		groupBy:     q.GroupBy,
		measures:    measureNames(q.Measures),
		keep:        compileResidual(q.Filter),
	}
	return newStream(ctx, plan, buckets, resume, s.cfg.MaxConcurrentBuckets), nil
}

// coarsest returns the coarsest of grains (month > day > hour > minute).
func coarsest(grains []types.Grain) types.Grain {
	order := []types.Grain{
		types.GrainMonth,
		types.GrainDay,
		types.GrainHour,
		types.GrainMinute,
	}
	for _, g := range order {
		if slices.Contains(grains, g) {
			return g
		}
	}
	return grains[0]
}

// measureNames returns the distinct measure names q computes.
func measureNames(measures []types.MeasureRef) []string {
	var out []string
	for _, m := range measures {
		if !slices.Contains(out, m.Name) {
			out = append(out, m.Name)
		}
	}
	return out
}

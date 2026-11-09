// Package outbox is the transactional-outbox relay: it leases due rows from an
// interfaces.OutboxRepository, delivers them through an interfaces.OutboxSink in
// bounded-concurrency chunks, and finalizes each row by its own outcome — sent,
// retried with capped exponential backoff and full jitter, or parked once its
// attempts are exhausted. With a drain budget a run keeps claiming while claims
// come back full, so a burst drains in one run instead of one batch per tick. It
// reports per-lane outcome counters, passes per run, and the lane's depth and lag.
// The repository and sink are injected (and decorated) by the caller: build the
// repository with repos/outbox.New over the store.
//
// Each pass is a saga, so the relay is a workflow: the claim commits in its own
// transaction, delivery runs with no transaction open (a remote call never holds one),
// and each row's finalize commits in its own transaction, all opened through the
// injected transaction manager.
package outbox

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// defaultBatchSize is the rows one relay pass claims.
const defaultBatchSize = 100

// defaultSendBatch is the rows one send carries.
const defaultSendBatch = 10

// defaultConcurrency is the concurrent sends of a relay pass.
const defaultConcurrency = 4

// defaultMaxAttempts is the attempts before a row is dead-lettered.
const defaultMaxAttempts = 10

// defaultMaxBackoff is the longest delay between attempts.
const defaultMaxBackoff = 5 * time.Minute

// RelayConfig tunes one relay; one relay drains one lane.
type RelayConfig struct {
	// Lane is the outbox lane drained.
	Lane string `yaml:"lane" mapstructure:"lane"`
	// BatchSize is the most rows one RunOnce claims.
	BatchSize int `yaml:"batch_size" mapstructure:"batch_size"`
	// SendBatch is the most records handed to one Sink.Send call.
	SendBatch int `yaml:"send_batch" mapstructure:"send_batch"`
	// Concurrency bounds how many Send calls run at once.
	Concurrency int `yaml:"concurrency" mapstructure:"concurrency"`
	// MaxAttempts is the attempt on which a still-failing record is parked.
	MaxAttempts int `yaml:"max_attempts" mapstructure:"max_attempts"`
	// BaseBackoff is the backoff unit: a record that failed attempt n waits up to
	// BaseBackoff·2^n before its next attempt.
	BaseBackoff time.Duration `yaml:"base_backoff" mapstructure:"base_backoff"`
	// MaxBackoff caps the backoff before jitter.
	MaxBackoff time.Duration `yaml:"max_backoff" mapstructure:"max_backoff"`
	// ParkOnPermanent parks a record on its first permanent failure (one
	// apperr.IsPermanent reports, e.g. a malformed payload the destination
	// rejects) instead of retrying it until MaxAttempts.
	ParkOnPermanent bool `yaml:"park_on_permanent" mapstructure:"park_on_permanent"`
	// DrainBudget bounds how long one RunOnce keeps claiming: while a claim returns a
	// full BatchSize, the run claims again until the budget has elapsed (the batch in
	// flight when it runs out still finishes). Zero, the default, claims once per run.
	DrainBudget time.Duration `yaml:"drain_budget" mapstructure:"drain_budget"`
	// Partitions is how many partitions the lane is split into (the store's
	// partitions); zero means 1, today's single relay per lane.
	Partitions int `yaml:"partitions" mapstructure:"partitions"`
	// Partition is the partition this relay drains, in [0, Partitions). Run one relay
	// per partition, each with its own leader election, so N relays drain N
	// partitions in parallel.
	Partition int `yaml:"partition" mapstructure:"partition"`
}

// DefaultRelayConfig returns a relay config for lane: claim 100 rows, send 10 at
// a time with 4 concurrent sends, park on the 10th attempt, back off from 1s up
// to 5m.
func DefaultRelayConfig(lane string) RelayConfig {
	return RelayConfig{
		Lane:        lane,
		BatchSize:   defaultBatchSize,
		SendBatch:   defaultSendBatch,
		Concurrency: defaultConcurrency,
		MaxAttempts: defaultMaxAttempts,
		BaseBackoff: time.Second,
		MaxBackoff:  defaultMaxBackoff,
	}
}

// Validate reports a config the relay cannot run with as CodeInvalidInput.
func (c RelayConfig) Validate() error {
	switch {
	case c.Lane == "":
		return apperr.New(apperr.CodeInvalidInput, "outbox relay: lane is required")
	case c.BatchSize <= 0, c.SendBatch <= 0, c.Concurrency <= 0, c.MaxAttempts <= 0:
		return apperr.New(
			apperr.CodeInvalidInput,
			"outbox relay: batch_size, send_batch, concurrency and max_attempts must be positive",
		)
	case c.BaseBackoff <= 0 || c.MaxBackoff < c.BaseBackoff:
		return apperr.New(
			apperr.CodeInvalidInput,
			"outbox relay: base_backoff must be positive and max_backoff at least base_backoff",
		)
	case c.DrainBudget < 0:
		return apperr.New(
			apperr.CodeInvalidInput,
			"outbox relay: drain_budget must not be negative",
		)
	case c.Partitions < 0 || c.Partition < 0 || c.Partition >= max(c.Partitions, 1):
		return apperr.New(
			apperr.CodeInvalidInput,
			"outbox relay: partition must be in [0, partitions)",
		)
	}

	return nil
}

// LoadRelayConfig reads the relay config for lane from loader's key section,
// overlaid on DefaultRelayConfig(lane), and validates it. An absent section
// yields the defaults; an unreadable or invalid one is CodeInvalidInput. Load
// through it so a bad section stops boot with a coded error, never NewRelay's
// panic.
func LoadRelayConfig(
	loader interfaces.ConfigLoader,
	key, lane string,
) (RelayConfig, error) {
	cfg := DefaultRelayConfig(lane)
	if loader.Get(key) != nil {
		err := loader.UnmarshalKey(key, &cfg)
		if err != nil {
			return RelayConfig{}, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load outbox relay config "+key,
			)
		}
	}

	err := cfg.Validate()
	if err != nil {
		return RelayConfig{}, err
	}

	return cfg, nil
}

// Option customizes a Relay beyond its config (the deterministic seams tests use).
type Option func(*Relay)

// WithClock sets the clock retry times are computed from (default time.Now).
func WithClock(now func() time.Time) Option { return func(r *Relay) { r.now = now } }

// WithJitter sets how the capped backoff is randomized (default retry.FullJitter,
// a uniform draw from [0, cap]).
func WithJitter(jitter func(time.Duration) time.Duration) Option {
	return func(r *Relay) { r.jitter = jitter }
}

// relayContract is the relay's one operation, the contract its undecorated workflow Base
// is built from: the relay only opens transactions through it.
type relayContract interface {
	// RunOnce runs one relay pass.
	RunOnce(ctx context.Context) error
}

// Relay moves outbox rows from a store to a sink. It is safe for concurrent
// RunOnce calls (the store's claim keeps them disjoint).
type Relay struct {
	// tx opens the claim's and each finalize's transaction.
	tx workflow.Base
	// store is the decorated repository over the outbox rows.
	store interfaces.OutboxRepository
	// sink delivers them.
	sink interfaces.OutboxSink
	// logger logs non-fatal failures (nil = off).
	logger interfaces.Logger
	// tracer spans each run (nil = off).
	tracer interfaces.Tracer
	// sent, retried and parked count outcomes by lane (nil = metrics off).
	sent, retried, parked interfaces.Counter
	// depth and lag report the lane's backlog by lane (nil = metrics off).
	depth, lag interfaces.Gauge
	// passes records how many claims each run made, by lane (nil = metrics off).
	passes interfaces.Histogram
	// now is the clock.
	now func() time.Time
	// jitter randomizes a capped backoff.
	jitter func(time.Duration) time.Duration
	// cfg is the validated config.
	cfg RelayConfig
}

// NewRelay builds a relay over store and sink whose writes commit through tx.
// logger, metrics and tracer may be nil. An invalid cfg or a nil tx, store or sink
// is a wiring bug and panics; build cfg with LoadRelayConfig (or call cfg.Validate
// first) to get a coded error instead.
func NewRelay(
	cfg RelayConfig,
	tx interfaces.TransactionManager,
	store interfaces.OutboxRepository,
	sink interfaces.OutboxSink,
	logger interfaces.Logger,
	metrics interfaces.Metrics,
	tracer interfaces.Tracer,
	opts ...Option,
) *Relay {
	err := cfg.Validate()
	if err != nil {
		panic(err)
	}

	if tx == nil || store == nil || sink == nil {
		panic(
			apperr.New(
				apperr.CodeInvalidInput,
				"outbox relay: tx, store and sink are required",
			),
		)
	}

	base, err := workflow.NewBase[relayContract](
		decorate.Fixed(decorate.Deps{}), "outbox relay "+cfg.Lane, tx,
	)
	if err != nil {
		panic(err)
	}

	r := &Relay{
		tx:  base,
		cfg: cfg, store: store, sink: sink, logger: logger, tracer: tracer,
		now: time.Now, jitter: retry.FullJitter,
	}
	if metrics != nil {
		r.registerMetrics(metrics)
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// RunOnce claims a batch, delivers it and finalizes every row; with a DrainBudget
// it repeats while claims come back full and the budget lasts. It then records the
// passes and refreshes the lane gauges. Each claim is called once (it is
// side-effecting, so it is never retried here) and commits before delivery starts;
// each finalize commits on its own, so one row's failure never rolls back another's.
// A finalize failure does not stop the other rows of its batch — the row's lease
// expires and it is re-claimed — but it ends the drain, and the first such error is
// returned. A failed claim ends the run with its error. A Stats failure is only
// logged; with metrics off Stats is skipped.
func (r *Relay) RunOnce(ctx context.Context) error {
	if r.tracer != nil {
		var span interfaces.Span

		ctx, span = r.tracer.Start(ctx, "outbox.relay.run")

		defer span.End()

		span.SetAttribute("outbox.lane", r.cfg.Lane)

		err := r.runIteration(ctx)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(interfaces.SpanStatusError, err.Error())
		}

		return err
	}

	return r.runIteration(ctx)
}

// registerMetrics registers the relay's outcome counters and backlog gauges, by lane.
func (r *Relay) registerMetrics(metrics interfaces.Metrics) {
	r.sent = metrics.Counter("outbox_sent_total", "Outbox records delivered.", textLane)
	r.retried = metrics.Counter(
		"outbox_retried_total",
		"Outbox deliveries rescheduled after a failure.",
		textLane,
	)
	r.parked = metrics.Counter(
		"outbox_parked_total",
		"Outbox records parked after exhausting their attempts.",
		textLane,
	)
	r.depth = metrics.Gauge("outbox_depth", "Outbox records pending delivery.", textLane)
	r.lag = metrics.Gauge(
		"outbox_lag_seconds",
		"Age of the oldest pending outbox record, in seconds.",
		textLane,
	)
	r.passes = metrics.Histogram(
		"outbox_relay_passes",
		"Claims one relay run made before its lane drained or its drain budget ran out.",
		// One claim (no backlog, or no drain budget) up to a long drain.
		[]float64{1, 2, 4, 8, 16, 32, 64, 128},
		textLane,
	)
}

// runIteration is RunOnce without the span.
func (r *Relay) runIteration(ctx context.Context) error {
	passes, err := r.drain(ctx)

	if passes > 0 {
		if r.passes != nil {
			r.passes.Observe(float64(passes), r.cfg.Lane)
		}

		r.refreshGauges(ctx)
	}

	return err
}

// drain runs passes until a claim fails, a pass errors or the drain budget ends,
// returning how many passes claimed successfully.
func (r *Relay) drain(ctx context.Context) (int, error) {
	start := r.now()
	passes := 0

	for {
		claimed, err := r.pass(ctx)
		if claimed < 0 {
			return passes, err // the claim failed; nothing was delivered this pass
		}

		passes++

		if err != nil || !r.drainMore(ctx, start, claimed) {
			return passes, err
		}
	}
}

// drainMore reports whether a run whose latest pass claimed that many rows should
// claim again: a drain budget is set and still lasts, the claim was full, and
// the run's context is live.
func (r *Relay) drainMore(ctx context.Context, start time.Time, claimed int) bool {
	return r.cfg.DrainBudget > 0 &&
		claimed >= r.cfg.BatchSize &&
		ctx.Err() == nil &&
		r.now().Sub(start) < r.cfg.DrainBudget
}

// pass claims one batch, delivers it and finalizes every row. It returns how many
// rows it claimed (-1 when the claim failed) and the claim's or first finalize's
// error.
func (r *Relay) pass(ctx context.Context) (int, error) {
	recs, err := r.tx.InTx(ctx, func(ctx context.Context) ([]types.OutboxRecord, error) {
		return r.store.Claim(ctx, r.cfg.Lane, r.cfg.Partition, r.cfg.BatchSize)
	})
	if err != nil {
		return -1, apperr.Wrap(
			err,
			apperr.CodeOr(err, apperr.CodeUnavailable),
			"outbox relay: claim",
		)
	}

	var (
		first firstError
		g     errgroup.Group
	)

	g.SetLimit(r.cfg.Concurrency)

	for start := 0; start < len(recs); start += r.cfg.SendBatch {
		chunk := recs[start:min(start+r.cfg.SendBatch, len(recs))]

		g.Go(func() error {
			first.keep(r.deliver(ctx, chunk))

			return nil
		})
	}

	_ = g.Wait() // workers report through keep, never through the group.

	return len(recs), first.err
}

// firstError keeps the first error reported across a pass's workers.
type firstError struct {
	// err is the first error kept.
	err error

	// mu guards err.
	mu sync.Mutex
}

// keep records the first of errs if none is kept yet.
func (f *firstError) keep(errs []error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err == nil && len(errs) > 0 {
		f.err = errs[0]
	}
}

// deliver sends one chunk and finalizes each of its rows, returning the finalize
// errors.
func (r *Relay) deliver(ctx context.Context, chunk []types.OutboxRecord) []error {
	results := alignedResults(r.sink.Send(ctx, chunk), len(chunk))

	var errs []error

	for i := range chunk {
		if interrupted(ctx, results[i]) {
			continue // the row stays leased and is re-claimed when its lease expires
		}

		err := r.finalize(ctx, &chunk[i], results[i])
		if err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

// alignedResults fails every record when the sink's result count does not match.
func alignedResults(results []error, n int) []error {
	if len(results) == n {
		return results
	}

	mismatch := apperr.New(
		apperr.CodeInternal,
		fmt.Sprintf(
			"outbox relay: sink returned %d results for %d records",
			len(results),
			n,
		),
	)

	out := make([]error, n)
	for i := range out {
		out[i] = mismatch
	}

	return out
}

// interrupted reports a send that failed only because the run's own context
// ended (shutdown or deadline); such a failure is not a delivery attempt.
func interrupted(ctx context.Context, sendErr error) bool {
	return sendErr != nil && ctx.Err() != nil &&
		(apperr.StdIs(sendErr, context.Canceled) ||
			apperr.StdIs(sendErr, context.DeadlineExceeded))
}

// finalize records one row's outcome in its own transaction: sent, parked on its
// last attempt (or on its first permanent failure with ParkOnPermanent), or
// rescheduled with jittered capped exponential backoff. The outcome is counted once
// the transaction commits.
func (r *Relay) finalize(
	ctx context.Context,
	rec *types.OutboxRecord,
	sendErr error,
) error {
	outcome, err := r.tx.InTx(ctx, func(ctx context.Context) (interfaces.Counter, error) {
		return r.record(ctx, rec, sendErr)
	})
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeOr(err, apperr.CodeUnavailable),
			"outbox relay: finalize "+rec.ID.String(),
		)
	}

	if outcome != nil {
		outcome.Inc(r.cfg.Lane)
	}

	return nil
}

// record writes one row's outcome and returns the counter it is counted on (nil with
// metrics off).
func (r *Relay) record(
	ctx context.Context,
	rec *types.OutboxRecord,
	sendErr error,
) (interfaces.Counter, error) {
	switch {
	case sendErr == nil:
		return r.sent, r.store.MarkSent(ctx, rec.ID)
	case rec.Attempts >= r.cfg.MaxAttempts ||
		(r.cfg.ParkOnPermanent && apperr.IsPermanent(sendErr)):
		return r.parked, r.store.Park(ctx, rec.ID, sendErr.Error())
	default:
		next := r.now().Add(r.jitter(r.backoff(rec.Attempts)))

		return r.retried, r.store.Retry(ctx, rec.ID, next, sendErr.Error())
	}
}

// backoff is min(BaseBackoff·2^attempts, MaxBackoff), computed without overflow.
func (r *Relay) backoff(attempts int) time.Duration {
	d := r.cfg.BaseBackoff
	for range attempts {
		if d >= r.cfg.MaxBackoff/maxBackoff2 {
			return r.cfg.MaxBackoff
		}

		d *= backoffFactor
	}

	return min(d, r.cfg.MaxBackoff)
}

// refreshGauges sets the lane's depth and lag from Stats; a failure is logged.
// With metrics off there is nothing to report, so Stats is not queried.
func (r *Relay) refreshGauges(ctx context.Context) {
	if r.depth == nil {
		return
	}

	stats, err := r.store.Stats(ctx, r.cfg.Lane)
	if err != nil {
		if r.logger != nil {
			r.logger.WithContext(ctx).
				Warn("outbox relay: stats failed", textLane, r.cfg.Lane, "error", err.Error())
		}

		return
	}

	lag := lag0p0
	if !stats.OldestPending.IsZero() {
		lag = max(r.now().Sub(stats.OldestPending).Seconds(), 0)
	}

	r.depth.Set(float64(stats.Pending), r.cfg.Lane)
	r.lag.Set(lag, r.cfg.Lane)
}

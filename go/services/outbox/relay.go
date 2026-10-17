// Package outbox is the transactional-outbox relay: it leases due rows from an
// interfaces.OutboxRepository, delivers them through an interfaces.OutboxSink in
// bounded-concurrency chunks, and finalizes each row by its own outcome — sent,
// retried with capped exponential backoff and full jitter, or parked once its
// attempts are exhausted. It reports per-lane outcome counters and the lane's
// depth and lag. The repository and sink are injected (and decorated) by the caller:
// build the repository with repository.Base.Outbox over the store.
package outbox

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
)

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
}

// DefaultRelayConfig returns a relay config for lane: claim 100 rows, send 10 at
// a time with 4 concurrent sends, park on the 10th attempt, back off from 1s up
// to 5m.
func DefaultRelayConfig(lane string) RelayConfig {
	return RelayConfig{
		Lane: lane, BatchSize: 100, SendBatch: 10, Concurrency: 4, MaxAttempts: 10,
		BaseBackoff: time.Second, MaxBackoff: 5 * time.Minute,
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
		if err := loader.UnmarshalKey(key, &cfg); err != nil {
			return RelayConfig{}, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				"load outbox relay config "+key,
			)
		}
	}
	if err := cfg.Validate(); err != nil {
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

// Relay moves outbox rows from a store to a sink. It is safe for concurrent
// RunOnce calls (the store's claim keeps them disjoint).
type Relay struct {
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
	// now is the clock.
	now func() time.Time
	// jitter randomizes a capped backoff.
	jitter func(time.Duration) time.Duration
	// cfg is the validated config.
	cfg RelayConfig
}

// NewRelay builds a relay over store and sink. logger, metrics and tracer may be
// nil. An invalid cfg or a nil store or sink is a wiring bug and panics; build
// cfg with LoadRelayConfig (or call cfg.Validate first) to get a coded error
// instead.
func NewRelay(
	cfg RelayConfig,
	store interfaces.OutboxRepository,
	sink interfaces.OutboxSink,
	logger interfaces.Logger,
	metrics interfaces.Metrics,
	tracer interfaces.Tracer,
	opts ...Option,
) *Relay {
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	if store == nil || sink == nil {
		panic(
			apperr.New(
				apperr.CodeInvalidInput,
				"outbox relay: store and sink are required",
			),
		)
	}
	r := &Relay{
		cfg: cfg, store: store, sink: sink, logger: logger, tracer: tracer,
		now: time.Now, jitter: retry.FullJitter,
	}
	if metrics != nil {
		r.sent = metrics.Counter("outbox_sent_total", "Outbox records delivered.", "lane")
		r.retried = metrics.Counter(
			"outbox_retried_total",
			"Outbox deliveries rescheduled after a failure.",
			"lane",
		)
		r.parked = metrics.Counter(
			"outbox_parked_total",
			"Outbox records parked after exhausting their attempts.",
			"lane",
		)
		r.depth = metrics.Gauge(
			"outbox_depth",
			"Outbox records pending delivery.",
			"lane",
		)
		r.lag = metrics.Gauge(
			"outbox_lag_seconds",
			"Age of the oldest pending outbox record, in seconds.",
			"lane",
		)
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// RunOnce claims one batch, delivers it and finalizes every row, then refreshes
// the lane gauges. The claim is called exactly once (it is side-effecting, so it
// is never retried here). A finalize failure does not stop the other rows — the
// row's lease expires and it is re-claimed — and the first such error is
// returned. A Stats failure is only logged; with metrics off Stats is skipped.
func (r *Relay) RunOnce(ctx context.Context) error {
	if r.tracer != nil {
		var span interfaces.Span
		ctx, span = r.tracer.Start(ctx, "outbox.relay.run")
		defer span.End()
		span.SetAttribute("outbox.lane", r.cfg.Lane)
		err := r.runOnce(ctx)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(interfaces.SpanStatusError, err.Error())
		}
		return err
	}
	return r.runOnce(ctx)
}

// runOnce is RunOnce without the span.
func (r *Relay) runOnce(ctx context.Context) error {
	recs, err := r.store.Claim(ctx, r.cfg.Lane, r.cfg.BatchSize)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeOr(err, apperr.CodeUnavailable),
			"outbox relay: claim",
		)
	}

	var (
		mu    sync.Mutex
		first error
	)
	keep := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if first == nil {
			first = err
		}
	}
	var g errgroup.Group
	g.SetLimit(r.cfg.Concurrency)
	for start := 0; start < len(recs); start += r.cfg.SendBatch {
		chunk := recs[start:min(start+r.cfg.SendBatch, len(recs))]
		g.Go(func() error {
			for _, err := range r.deliver(ctx, chunk) {
				keep(err)
			}
			return nil
		})
	}
	_ = g.Wait() // workers report through keep, never through the group.

	r.refreshGauges(ctx)
	return first
}

// deliver sends one chunk and finalizes each of its rows, returning the finalize
// errors.
func (r *Relay) deliver(ctx context.Context, chunk []types.OutboxRecord) []error {
	results := r.sink.Send(ctx, chunk)
	if len(results) != len(chunk) {
		mismatch := apperr.New(
			apperr.CodeInternal,
			fmt.Sprintf(
				"outbox relay: sink returned %d results for %d records",
				len(results),
				len(chunk),
			),
		)
		results = make([]error, len(chunk))
		for i := range results {
			results[i] = mismatch
		}
	}
	var errs []error
	for i := range chunk {
		if interrupted(ctx, results[i]) {
			continue // the row stays leased and is re-claimed when its lease expires
		}
		if err := r.finalize(ctx, &chunk[i], results[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// interrupted reports a send that failed only because the run's own context
// ended (shutdown or deadline); such a failure is not a delivery attempt.
func interrupted(ctx context.Context, sendErr error) bool {
	return sendErr != nil && ctx.Err() != nil &&
		(apperr.StdIs(sendErr, context.Canceled) ||
			apperr.StdIs(sendErr, context.DeadlineExceeded))
}

// finalize records one row's outcome: sent, parked on its last attempt (or on
// its first permanent failure with ParkOnPermanent), or rescheduled with
// jittered capped exponential backoff.
func (r *Relay) finalize(
	ctx context.Context,
	rec *types.OutboxRecord,
	sendErr error,
) error {
	lane := r.cfg.Lane
	var err error
	switch {
	case sendErr == nil:
		err = r.store.MarkSent(ctx, rec.ID)
		inc(r.sent, lane, err)
	case rec.Attempts >= r.cfg.MaxAttempts ||
		(r.cfg.ParkOnPermanent && apperr.IsPermanent(sendErr)):
		err = r.store.Park(ctx, rec.ID, sendErr.Error())
		inc(r.parked, lane, err)
	default:
		next := r.now().Add(r.jitter(r.backoff(rec.Attempts)))
		err = r.store.Retry(ctx, rec.ID, next, sendErr.Error())
		inc(r.retried, lane, err)
	}
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeOr(err, apperr.CodeUnavailable),
			"outbox relay: finalize "+rec.ID.String(),
		)
	}
	return nil
}

// backoff is min(BaseBackoff·2^attempts, MaxBackoff), computed without overflow.
func (r *Relay) backoff(attempts int) time.Duration {
	d := r.cfg.BaseBackoff
	for range attempts {
		if d >= r.cfg.MaxBackoff/2 {
			return r.cfg.MaxBackoff
		}
		d *= 2
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
				Warn("outbox relay: stats failed", "lane", r.cfg.Lane, "error", err.Error())
		}
		return
	}
	lag := 0.0
	if !stats.OldestPending.IsZero() {
		lag = max(r.now().Sub(stats.OldestPending).Seconds(), 0)
	}
	r.depth.Set(float64(stats.Pending), r.cfg.Lane)
	r.lag.Set(lag, r.cfg.Lane)
}

// inc counts a finalize outcome when it was recorded.
func inc(c interfaces.Counter, lane string, err error) {
	if c != nil && err == nil {
		c.Inc(lane)
	}
}

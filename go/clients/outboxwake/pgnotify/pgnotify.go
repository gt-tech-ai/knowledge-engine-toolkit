// Package pgnotify is the OutboxWaker backed by PostgreSQL LISTEN/NOTIFY: one dedicated
// connection, outside any pool, LISTENs on a channel the outbox store notifies on
// enqueue, and each notification becomes a types.OutboxWake. Notifications are delivered
// at most once and not while the connection is down, so the waker emits an All wake
// after every reconnect and a relay still polls; the wake only shortens its sleep.
package pgnotify

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
)

const (
	// DefaultBuffer is the wake buffer a zero Config.Buffer selects.
	DefaultBuffer = 64

	// minBackoff is the first reconnect delay ceiling; it doubles up to maxBackoff.
	minBackoff = 100 * time.Millisecond

	// maxBackoff caps the reconnect delay ceiling.
	maxBackoff = 30 * time.Second

	// gaugeUp and gaugeDown are the outbox_wake_connected values.
	gaugeUp   float64 = 1
	gaugeDown float64 = 0

	// backoffFactor is how much the reconnect delay ceiling grows per failed attempt.
	backoffFactor = 2

	// closeTimeout bounds how long Close waits for the listener to stop.
	closeTimeout = time.Second

	// maxChannelLen is PostgreSQL's identifier length limit.
	maxChannelLen = 63

	// metricWake counts wakes by source.
	metricWake = "outbox_wake_total"

	// metricConnected is 1 while the listener holds a live LISTEN, else 0.
	metricConnected = "outbox_wake_connected"

	// sourceNotify labels a wake produced by a notification.
	sourceNotify = "notify"

	// sourceReconnect labels the All wake emitted after a reconnect.
	sourceReconnect = "reconnect"

	// sourceDropped labels a wake discarded because the buffer was full.
	sourceDropped = "dropped"
)

// Config tunes the waker.
type Config struct {
	// Channel is the PostgreSQL channel to LISTEN on; a plain lowercase identifier.
	Channel string

	// Buffer is the capacity of the wake channel; zero means DefaultBuffer, and a
	// negative value is invalid.
	Buffer int
}

// Waker listens on one channel and delivers a wake per notification.
type Waker struct {
	wakes     chan types.OutboxWake
	logger    interfaces.Logger
	wakeTotal interfaces.Counter
	connected interfaces.Gauge
	cancel    context.CancelFunc
	done      chan struct{}
	dsn       string
	// listenSQL is the LISTEN statement for the validated channel, built once in New.
	// LISTEN takes no bound parameter, so the channel is quoted as an identifier there.
	listenSQL string
	mu        sync.Mutex
	started   bool
}

// New builds a waker over dsn with no I/O. A missing DSN, a channel that is not a plain
// identifier, or a negative buffer is CodeInvalidInput. logger and metrics may be nil.
func New(
	cfg Config,
	dsn string,
	logger interfaces.Logger,
	metrics interfaces.Metrics,
) (*Waker, error) {
	if dsn == "" {
		return nil, apperr.InvalidInput("outbox waker pg_notify: dsn is required")
	}

	if !validChannel(cfg.Channel) {
		return nil, apperr.InvalidInput(
			"outbox waker pg_notify: channel must be a plain lowercase identifier, got " +
				strconv.Quote(cfg.Channel),
		)
	}

	if cfg.Buffer < 0 {
		return nil, apperr.InvalidInput(
			"outbox waker pg_notify: buffer must not be negative",
		)
	}

	buffer := cfg.Buffer
	if buffer == 0 {
		buffer = DefaultBuffer
	}

	waker := &Waker{
		wakes:     make(chan types.OutboxWake, buffer),
		logger:    logger,
		wakeTotal: nil,
		connected: nil,
		cancel:    nil,
		done:      make(chan struct{}),
		dsn:       dsn,
		listenSQL: listenStatement(cfg.Channel),
		mu:        sync.Mutex{},
		started:   false,
	}

	if metrics != nil {
		waker.wakeTotal = metrics.Counter(metricWake, "Outbox wakes by source.", "source")
		waker.connected = metrics.Gauge(
			metricConnected, "1 while the outbox waker holds a LISTEN connection.",
		)
	}

	return waker, nil
}

// validChannel reports whether name is a lowercase identifier PostgreSQL accepts
// unquoted: a letter or underscore first, then letters, digits or underscores.
func validChannel(name string) bool {
	if name == "" || len(name) > maxChannelLen {
		return false
	}

	for i := range len(name) {
		c := name[i]

		switch {
		case c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}

	return true
}

// Wakes returns the wake channel; it is never closed.
func (w *Waker) Wakes() <-chan types.OutboxWake { return w.wakes }

// Start dials the dedicated connection and LISTENs, then keeps it connected in the
// background until ctx ends or Close is called. A first connect that fails is
// CodeUnavailable and nothing is left running; Start twice is CodeConflict.
func (w *Waker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		return apperr.New(apperr.CodeConflict, "outbox waker pg_notify: already started")
	}

	conn, err := w.connect(ctx)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	w.cancel = cancel
	w.started = true

	go w.run(runCtx, ctx, conn)

	return nil
}

// Close stops the listener and waits up to a second for its connection to close.
// It is safe to call repeatedly and before Start.
func (w *Waker) Close() error {
	w.mu.Lock()

	cancel, started := w.cancel, w.started

	w.mu.Unlock()

	if !started {
		return nil
	}

	cancel()

	select {
	case <-w.done:
	case <-time.After(closeTimeout):
		w.warn("outbox waker did not stop in time", "timeout", closeTimeout)
	}

	return nil
}

// listenStatement returns the LISTEN statement for channel, which New has already
// checked with validChannel; the identifier is quoted as well, so it can only name a
// channel.
func listenStatement(channel string) string {
	return "LISTEN " + pgx.Identifier{channel}.Sanitize()
}

// connect dials a dedicated connection and LISTENs on the channel.
func (w *Waker) connect(ctx context.Context) (*pgx.Conn, error) {
	conn, err := pgx.Connect(ctx, w.dsn)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"outbox waker pg_notify: connect",
		)
	}

	_, err = conn.Exec(ctx, w.listenSQL)
	if err != nil {
		_ = conn.Close(context.WithoutCancel(ctx))

		return nil, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"outbox waker pg_notify: listen",
		)
	}

	w.setConnected(gaugeUp)

	return conn, nil
}

// run drains notifications from conn and reconnects when it fails, until runCtx ends or
// the caller's startCtx does. It closes done on exit.
func (w *Waker) run(runCtx, startCtx context.Context, conn *pgx.Conn) {
	defer close(w.done)

	stop := context.AfterFunc(startCtx, w.cancel)
	defer stop()

	for conn != nil {
		err := w.listen(runCtx, conn)

		w.setConnected(gaugeDown)

		_ = conn.Close(context.WithoutCancel(runCtx))

		if runCtx.Err() != nil {
			return
		}

		w.warn("outbox waker lost its connection", "error", err)

		conn = w.reconnect(runCtx)
		if conn != nil {
			w.emit(types.OutboxWake{Partition: 0, All: true}, sourceReconnect)
		}
	}
}

// listen delivers each notification on conn until the connection fails or ctx ends.
func (w *Waker) listen(ctx context.Context, conn *pgx.Conn) error {
	for {
		note, err := conn.WaitForNotification(ctx)
		if err != nil {
			return apperr.Wrap(
				err,
				apperr.CodeUnavailable,
				"outbox waker pg_notify: wait",
			)
		}

		w.emit(parse(note.Payload), sourceNotify)
	}
}

// reconnect retries connect with exponential backoff and full jitter until it succeeds,
// and returns nil when ctx ends first.
func (w *Waker) reconnect(ctx context.Context) *pgx.Conn {
	ceiling := minBackoff

	for {
		timer := time.NewTimer(retry.FullJitter(ceiling))

		select {
		case <-ctx.Done():
			timer.Stop()

			return nil
		case <-timer.C:
		}

		conn, err := w.connect(ctx)
		if err == nil {
			w.info("outbox waker reconnected")

			return conn
		}

		w.warn("outbox waker reconnect failed", "error", err)

		ceiling = min(ceiling*backoffFactor, maxBackoff)
	}
}

// emit delivers wake without blocking: a full buffer drops it and counts the drop.
func (w *Waker) emit(wake types.OutboxWake, source string) {
	select {
	case w.wakes <- wake:
		w.count(source)
	default:
		w.count(sourceDropped)
		w.warn("outbox wake dropped: buffer full")
	}
}

// parse reads a notification payload, "<lane>" or "<lane>:<partition>". A payload with
// no valid partition wakes every partition.
func parse(payload string) types.OutboxWake {
	_, tail, found := strings.Cut(payload, ":")
	if !found {
		return types.OutboxWake{Partition: 0, All: true}
	}

	partition, err := strconv.Atoi(tail)
	if err != nil || partition < 0 {
		return types.OutboxWake{Partition: 0, All: true}
	}

	return types.OutboxWake{Partition: partition, All: false}
}

// count increments outbox_wake_total for source.
func (w *Waker) count(source string) {
	if w.wakeTotal != nil {
		w.wakeTotal.Inc(source)
	}
}

// setConnected sets outbox_wake_connected to value (gaugeUp or gaugeDown).
func (w *Waker) setConnected(value float64) {
	if w.connected != nil {
		w.connected.Set(value)
	}
}

// info logs at info level when a logger is set.
func (w *Waker) info(msg string, kv ...any) {
	if w.logger != nil {
		w.logger.Info(msg, kv...)
	}
}

// warn logs at warn level when a logger is set.
func (w *Waker) warn(msg string, kv ...any) {
	if w.logger != nil {
		w.logger.Warn(msg, kv...)
	}
}

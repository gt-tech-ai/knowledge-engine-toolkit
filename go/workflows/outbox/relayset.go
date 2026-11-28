package outbox

import (
	"strconv"
	"time"

	"dario.cat/mergo"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/loop"
)

// defaultRelayInterval is how often a lane's relay polls when its row sets no interval.
const defaultRelayInterval = time.Second

// LaneConfig is one lane of a relay set: which lane to drain, which sink to deliver to,
// how often to poll, and the relay's overrides of DefaultRelayConfig.
type LaneConfig struct {
	// Lane is the outbox lane drained; it names the relays built for it.
	Lane string `yaml:"lane" mapstructure:"lane"`
	// Sink names the entry of RelaySetDeps.Sinks that delivers the lane's rows.
	Sink string `yaml:"sink" mapstructure:"sink"`
	// Relay overrides DefaultRelayConfig field by field: a zero field keeps the default.
	// Relay.Partitions sets how many relays split the lane; Relay.Partition is assigned
	// by the set and must stay zero.
	Relay RelayConfig `yaml:"relay" mapstructure:"relay"`
	// Interval is how often each of the lane's relays polls; zero means one second.
	Interval time.Duration `yaml:"interval" mapstructure:"interval"`
}

// RelaySetConfig lists the lanes a deployable relays.
type RelaySetConfig struct {
	// Lanes are the lanes to drain, each lane named once. An empty list is a valid set
	// of no relays.
	Lanes []LaneConfig `yaml:"lanes" mapstructure:"lanes"`
}

// merged returns the relay config of the lane's partition 0: DefaultRelayConfig(lane)
// overridden by the row's non-zero Relay fields.
func (l *LaneConfig) merged() (RelayConfig, error) {
	cfg := DefaultRelayConfig(l.Lane)

	override := l.Relay

	override.Lane = l.Lane

	err := mergo.Merge(&cfg, override, mergo.WithOverride)
	if err != nil {
		return RelayConfig{}, apperr.Wrap(err, apperr.CodeInternal, "merge relay config")
	}

	return cfg, nil
}

// check reports what makes the row unrunnable, as CodeInvalidInput.
func (l *LaneConfig) check() error {
	switch {
	case l.Lane == "":
		return apperr.New(apperr.CodeInvalidInput, "lane is required")
	case l.Sink == "":
		return apperr.New(apperr.CodeInvalidInput, "sink is required")
	case l.Interval < 0:
		return apperr.New(apperr.CodeInvalidInput, "interval must not be negative")
	case l.Relay.Partitions < 0:
		return apperr.New(
			apperr.CodeInvalidInput,
			"relay.partitions must not be negative",
		)
	case l.Relay.Partition != 0:
		return apperr.New(
			apperr.CodeInvalidInput,
			"relay.partition is assigned by the set and must be zero",
		)
	}

	cfg, err := l.merged()
	if err != nil {
		return err
	}

	return cfg.Validate()
}

// Validate reports a lane list the set cannot run as CodeInvalidInput naming the bad
// row's index: an empty lane or sink, a repeated lane, a negative interval or partition
// count, a preset partition, or a relay config that fails RelayConfig.Validate once
// merged over the defaults.
func (c *RelaySetConfig) Validate() error {
	seen := make(map[string]int, len(c.Lanes))

	for index := range c.Lanes {
		lane := &c.Lanes[index]
		label := "lanes[" + strconv.Itoa(index) + "]"

		err := lane.check()
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInvalidInput, label)
		}

		first, dup := seen[lane.Lane]
		if dup {
			return apperr.New(
				apperr.CodeInvalidInput,
				label+": lane "+lane.Lane+" repeats lanes["+strconv.Itoa(first)+"]",
			)
		}

		seen[lane.Lane] = index
	}

	return nil
}

// RelaySetDeps are the collaborators every relay of a set shares. Logger, Metrics and
// Tracer may be nil.
type RelaySetDeps struct {
	// TransactionManager opens each relay's claim and finalize transactions.
	TransactionManager interfaces.TransactionManager
	// Store is the decorated outbox repository every relay claims from.
	Store interfaces.OutboxRepository
	// Sinks are the delivery backends by name; a lane's Sink picks one.
	Sinks map[string]interfaces.OutboxSink
	// Logger logs the relays' non-fatal failures.
	Logger interfaces.Logger
	// Metrics registers the relays' counters, gauges and histograms.
	Metrics interfaces.Metrics
	// Tracer spans each relay pass.
	Tracer interfaces.Tracer
}

// RelayEntry is one relay of a set with the schedule it runs on.
type RelayEntry struct {
	// Relay runs one pass.
	Relay interfaces.OutboxRelay
	// Name labels the relay's loop in logs: "<lane>-<partition>".
	Name string
	// Lane is the outbox lane the relay drains.
	Lane string
	// Partition is the lane partition the relay claims.
	Partition int
	// Interval is how often the relay polls.
	Interval time.Duration
}

// RelaySet is every relay a deployable runs, one per lane partition.
type RelaySet []RelayEntry

// NewRelaySet builds one relay per partition of every lane in cfg, over deps. An
// invalid cfg, a nil TransactionManager or Store, or a lane naming a sink missing from
// deps.Sinks is CodeInvalidInput, so a bad config stops boot with a coded error rather
// than NewRelay's panic. opts apply to every relay.
func NewRelaySet(
	cfg *RelaySetConfig,
	deps *RelaySetDeps,
	opts ...Option,
) (RelaySet, error) {
	err := cfg.Validate()
	if err != nil {
		return nil, err
	}

	if deps.TransactionManager == nil || deps.Store == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"relay set: transaction manager and store are required",
		)
	}

	set := make(RelaySet, 0, len(cfg.Lanes))

	for index := range cfg.Lanes {
		entries, laneErr := laneEntries(&cfg.Lanes[index], deps, opts)
		if laneErr != nil {
			return nil, laneErr
		}

		set = append(set, entries...)
	}

	return set, nil
}

// laneEntries builds the relays of one validated lane row, one per partition.
func laneEntries(
	lane *LaneConfig,
	deps *RelaySetDeps,
	opts []Option,
) ([]RelayEntry, error) {
	sink, ok := deps.Sinks[lane.Sink]
	if !ok {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"lane "+lane.Lane+": unknown sink "+lane.Sink,
		)
	}

	cfg, err := lane.merged()
	if err != nil {
		return nil, err
	}

	interval := lane.Interval
	if interval == 0 {
		interval = defaultRelayInterval
	}

	partitions := max(cfg.Partitions, 1)
	entries := make([]RelayEntry, 0, partitions)

	for partition := range partitions {
		cfg.Partition = partition

		relay := NewRelay(
			&cfg, deps.TransactionManager, deps.Store, sink,
			deps.Logger, deps.Metrics, deps.Tracer, opts...,
		)

		entries = append(entries, RelayEntry{
			Relay:     relay,
			Name:      lane.Lane + "-" + strconv.Itoa(partition),
			Lane:      lane.Lane,
			Partition: partition,
			Interval:  interval,
		})
	}

	return entries, nil
}

// Group wraps every relay in a loop.Ticker that runs one pass at start and then once
// per entry interval, and returns the group a deployable starts and stops. A nil logger
// is a wiring bug and panics, as loop.NewTicker does.
func (s RelaySet) Group(logger interfaces.Logger) (*loop.Group, error) {
	group := loop.NewGroup(logger)

	for _, entry := range s {
		ticker, err := loop.NewTicker(
			entry.Name,
			loop.Pass(entry.Relay.RunOnce),
			loop.Config{Interval: entry.Interval, RunAtStart: true, ErrorBackoff: 0},
			logger,
		)
		if err != nil {
			return nil, err
		}

		group.Add(entry.Name, ticker)
	}

	return group, nil
}

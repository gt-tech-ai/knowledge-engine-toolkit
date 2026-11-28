package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/outbox"
)

const (
	// relaySetPrimary and relaySetSecondary name the two sinks a relay set test wires.
	relaySetPrimary   = "primary"
	relaySetSecondary = "secondary"
	// relaySetDocsLane is the second lane of a relay set test.
	relaySetDocsLane = "docs"
	// relaySetPartitions is how many partitions the audit lane of a set test drains.
	relaySetPartitions = 3
	// relaySetBatch is the batch size the audit lane overrides.
	relaySetBatch = 7
	// relaySetLastPartition is the last partition of the audit lane.
	relaySetLastPartition = relaySetPartitions - 1
	// relaySetEntries is the relays two lanes of 3 and 1 partitions make.
	relaySetEntries = relaySetPartitions + 1
	// relaySetInterval is the poll interval the audit lane overrides.
	relaySetInterval = 2 * time.Second
)

// relaySetLane builds a valid lane row for lane on the primary sink.
func relaySetLane(lane string, edit func(*outbox.LaneConfig)) outbox.LaneConfig {
	return fixtures.Build(func(row *outbox.LaneConfig) {
		row.Lane = lane
		row.Sink = relaySetPrimary

		if edit != nil {
			edit(row)
		}
	})
}

// TestRelaySetConfig_RefusesWhatTheSetCannotRun tests RelaySetConfig.Validate.
//
// Why this test is important:
//   - A lane with no name, no sink or a relay config that cannot run would otherwise
//     panic in NewRelay or drain nothing, at boot; each is refused with the index of the
//     bad row so the operator edits the right line
//
// What it tests:
//   - a valid two-lane set validates; an empty lane, an empty sink, a repeated lane, a
//     negative partition count, a negative interval, a preset partition and a relay
//     config that fails its own validation are each CodeInvalidInput naming lanes[1]
func TestRelaySetConfig_RefusesWhatTheSetCannotRun(t *testing.T) {
	t.Parallel()

	first := relaySetLane(textAudit, nil)
	valid := outbox.RelaySetConfig{
		Lanes: []outbox.LaneConfig{first, relaySetLane(relaySetDocsLane, nil)},
	}
	require.NoError(t, valid.Validate())

	bad := map[string]outbox.LaneConfig{
		"lane is required": relaySetLane("", nil),
		"sink is required": relaySetLane(relaySetDocsLane, func(row *outbox.LaneConfig) {
			row.Sink = ""
		}),
		"repeats": relaySetLane(textAudit, nil),
		"partitions": relaySetLane(relaySetDocsLane, func(row *outbox.LaneConfig) {
			row.Relay.Partitions = -1
		}),
		"interval": relaySetLane(relaySetDocsLane, func(row *outbox.LaneConfig) {
			row.Interval = -time.Second
		}),
		"partition is assigned": relaySetLane(
			relaySetDocsLane,
			func(row *outbox.LaneConfig) { row.Relay.Partition = 1 },
		),
		"max_backoff": relaySetLane(relaySetDocsLane, func(row *outbox.LaneConfig) {
			row.Relay.MaxBackoff = time.Millisecond
		}),
	}

	for want, row := range bad {
		cfg := outbox.RelaySetConfig{Lanes: []outbox.LaneConfig{first, row}}

		err := cfg.Validate()
		require.Error(t, err, want)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), want)
		assert.Contains(t, err.Error(), "lanes[1]", want)
		assert.Contains(t, err.Error(), want)
	}
}

// relaySetFixture builds a two-lane set (audit across three partitions with overrides,
// docs with defaults on the secondary sink) over a store mock.
func relaySetFixture(
	t *testing.T,
	store *mocks.MockOutboxStore,
) (outbox.RelaySet, error) {
	t.Helper()

	ctrl := gomock.NewController(t)
	cfg := outbox.RelaySetConfig{Lanes: []outbox.LaneConfig{
		relaySetLane(textAudit, func(row *outbox.LaneConfig) {
			row.Interval = relaySetInterval
			row.Relay.Partitions = relaySetPartitions
			row.Relay.BatchSize = relaySetBatch
		}),
		relaySetLane(relaySetDocsLane, func(row *outbox.LaneConfig) {
			row.Sink = relaySetSecondary
		}),
	}}
	deps := fixtures.Build(func(row *outbox.RelaySetDeps) {
		row.TransactionManager = relayTx(t)
		row.Store = store
		row.Sinks = map[string]interfaces.OutboxSink{
			relaySetPrimary:   mocks.NewMockOutboxSink(ctrl),
			relaySetSecondary: mocks.NewMockOutboxSink(ctrl),
		}
	})

	return outbox.NewRelaySet(&cfg, &deps, fixedClock())
}

// TestNewRelaySet_ExpandsLanesIntoNamedPartitionedRelays tests NewRelaySet.
//
// Why this test is important:
//   - The set is how a deployable gets one relay per lane partition from config; a
//     lane that kept its overrides off a relay, or a partition that claimed another's
//     rows, would drain the wrong rows or none
//
// What it tests:
//   - lane audit (3 partitions, batch 7, 2s) and lane docs (defaults) become four
//     entries named audit-0, audit-1, audit-2 and docs-0, each with its lane, partition
//     and interval (docs gets the 1s default)
//   - the last audit relay claims partition 2 of lane audit with the overridden batch
//     size, and a docs relay claims partition 0 with the default batch of 100
func TestNewRelaySet_ExpandsLanesIntoNamedPartitionedRelays(t *testing.T) {
	t.Parallel()

	store := mocks.NewMockOutboxStore(gomock.NewController(t))

	set, err := relaySetFixture(t, store)
	require.NoError(t, err)
	require.Len(t, set, relaySetEntries)

	names := make([]string, 0, len(set))
	for _, entry := range set {
		names = append(names, entry.Name)
	}

	assert.Equal(t, []string{"audit-0", "audit-1", "audit-2", "docs-0"}, names)
	assert.Equal(t, textAudit, set[relaySetLastPartition].Lane)
	assert.Equal(t, relaySetLastPartition, set[relaySetLastPartition].Partition)
	assert.Equal(t, relaySetInterval, set[relaySetLastPartition].Interval)
	assert.Equal(t, time.Second, set[relaySetPartitions].Interval)

	store.EXPECT().Claim(gomock.Any(), textAudit, relaySetLastPartition, relaySetBatch).
		Return(nil, nil)
	store.EXPECT().Claim(gomock.Any(), relaySetDocsLane, 0, batchSize).Return(nil, nil)

	require.NoError(t, set[relaySetLastPartition].Relay.RunOnce(context.Background()))
	require.NoError(t, set[relaySetPartitions].Relay.RunOnce(context.Background()))
}

// TestNewRelaySet_RefusesAnUnwiredSet tests NewRelaySet's wiring checks.
//
// Why this test is important:
//   - A lane naming a sink the deployable never built, or a set built without a store,
//     would panic or send nowhere at runtime; boot must stop with a coded error
//
// What it tests:
//   - an unknown sink is CodeInvalidInput naming the lane and the sink; a nil store or
//     transaction manager is CodeInvalidInput; an invalid config is refused before any
//     relay is built
func TestNewRelaySet_RefusesAnUnwiredSet(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	sinks := map[string]interfaces.OutboxSink{
		relaySetPrimary: mocks.NewMockOutboxSink(ctrl),
	}
	cfg := outbox.RelaySetConfig{Lanes: []outbox.LaneConfig{
		relaySetLane(textAudit, func(row *outbox.LaneConfig) { row.Sink = "ghost" }),
	}}

	wired := func(edit func(*outbox.RelaySetDeps)) outbox.RelaySetDeps {
		return fixtures.Build(func(row *outbox.RelaySetDeps) {
			row.TransactionManager = relayTx(t)
			row.Store = store
			row.Sinks = sinks
			edit(row)
		})
	}

	deps := wired(func(*outbox.RelaySetDeps) {})
	_, err := outbox.NewRelaySet(&cfg, &deps)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "lane audit: unknown sink ghost")

	cfg.Lanes[0].Sink = relaySetPrimary

	for _, deps := range []outbox.RelaySetDeps{
		wired(func(row *outbox.RelaySetDeps) { row.Store = nil }),
		wired(func(row *outbox.RelaySetDeps) { row.TransactionManager = nil }),
	} {
		_, err = outbox.NewRelaySet(&cfg, &deps)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	}

	cfg.Lanes[0].Lane = ""
	deps = wired(func(*outbox.RelaySetDeps) {})
	_, err = outbox.NewRelaySet(&cfg, &deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lanes[0]")
}

// TestRelaySet_GroupRunsEveryRelayOnItsInterval tests RelaySet.Group.
//
// Why this test is important:
//   - The group is what a deployable starts and stops; a relay left out of it would
//     never drain its partition, and a ticker that skipped the startup pass would leave
//     rows waiting a full interval after every deploy
//
// What it tests:
//   - each entry's relay runs once at start and again after its own interval, the
//     slower lane not before its interval has passed; Stop returns once every ticker
//     has ended. Time is synthetic (synctest), so the test waits on no wall clock
func TestRelaySet_GroupRunsEveryRelayOnItsInterval(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctrl := gomock.NewController(t)

		var fast, slow atomic.Int64

		relayCounting := func(runs *atomic.Int64) interfaces.OutboxRelay {
			relay := mocks.NewMockOutboxRelay(ctrl)
			relay.EXPECT().RunOnce(gomock.Any()).AnyTimes().DoAndReturn(
				func(context.Context) error {
					runs.Add(1)

					return nil
				})

			return relay
		}

		set := outbox.RelaySet{
			{
				Name: "fast-0", Lane: "fast", Partition: 0, Interval: time.Second,
				Relay: relayCounting(&fast),
			},
			{
				Name: "slow-0", Lane: "slow", Partition: 0, Interval: time.Minute,
				Relay: relayCounting(&slow),
			},
		}

		group, err := set.Group(quietLogger(t))
		require.NoError(t, err)
		require.NoError(t, group.Start(t.Context()))
		synctest.Wait()
		assert.Equal(t, int64(1), fast.Load())
		assert.Equal(t, int64(1), slow.Load())

		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal(t, int64(2), fast.Load())
		assert.Equal(t, int64(1), slow.Load())

		require.NoError(t, group.Stop(t.Context()))
	})
}

package outboxtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Harness is what the conformance suite drives: the store under test plus the
// producer-side insert the OutboxStore port does not carry.
type Harness struct {
	// Store is the OutboxStore under test.
	Store interfaces.OutboxStore
	// Enqueue inserts a pending record that is due immediately.
	Enqueue func(ctx context.Context, rec *types.OutboxRecord) error
}

// Run checks the OutboxStore contract the relay relies on. Each case uses its
// own lane, so one store (and table) serves them all. The store's claim lease
// must outlast the suite's few seconds, and Park / MarkSent must accept a row
// that is pending but not currently claimed.
//
// Why these checks matter:
//   - The relay is only at-least-once and parallel-safe if the store keeps
//     concurrent claims disjoint, honours each row's next attempt time, and
//     never re-delivers a sent or parked row.
//
// What they assert:
//   - concurrent claims return every due row exactly once;
//   - a claimed row is leased (a second claim does not return it), and a row
//     retried into the future stays hidden while one retried into the past is
//     claimable again with its attempt count bumped;
//   - sent and parked rows are never claimed, and Stats counts pending and
//     parked rows and reports the oldest pending row's creation time.
func Run(t *testing.T, h Harness) {
	t.Helper()
	t.Run("ConcurrentClaimsAreDisjoint", h.concurrentClaimsAreDisjoint)
	t.Run("NextAttemptAtIsHonoured", h.nextAttemptAtIsHonoured)
	t.Run("SentAndParkedAreNotClaimed", h.sentAndParkedAreNotClaimed)
	t.Run("StatsCountsPendingAndParked", h.statsCountsPendingAndParked)
}

// seed enqueues n records on lane, created a second apart (oldest first), and
// returns their ids in creation order.
func seed(t *testing.T, h Harness, lane string, n int, oldest time.Time) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, n)
	for i := range n {
		ids[i] = uuid.New()
		rec := types.OutboxRecord{
			ID: ids[i], Lane: lane, Tenant: "org-1", Key: "k", Payload: []byte(`{"n":1}`),
			CreatedAt: oldest.Add(time.Duration(i) * time.Second),
		}
		require.NoError(t, h.Enqueue(context.Background(), &rec))
	}
	return ids
}

// newLane returns a lane no other case uses.
func newLane() string { return "conformance-" + uuid.NewString() }

// concurrentClaimsAreDisjoint claims 50 rows from 5 goroutines.
func (h Harness) concurrentClaimsAreDisjoint(t *testing.T) {
	ctx, lane := context.Background(), newLane()
	want := seed(t, h, lane, 50, time.Now().Add(-time.Hour))

	var (
		mu   sync.Mutex
		got  = map[uuid.UUID]int{}
		errs []error
		wg   sync.WaitGroup
	)
	for range 5 {
		wg.Go(func() {
			recs, err := h.Store.Claim(ctx, lane, 10)
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, err) // checked after Wait: FailNow is test-goroutine only
			for i := range recs {
				got[recs[i].ID]++
			}
		})
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, got, len(want))
	for _, id := range want {
		require.Equalf(t, 1, got[id], "row %s claimed %d times", id, got[id])
	}
}

// nextAttemptAtIsHonoured checks the lease and Retry's schedule.
func (h Harness) nextAttemptAtIsHonoured(t *testing.T) {
	ctx, lane := context.Background(), newLane()
	ids := seed(t, h, lane, 2, time.Now().Add(-time.Hour))

	first, err := h.Store.Claim(ctx, lane, 10)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.Equal(t, 1, first[0].Attempts)

	again, err := h.Store.Claim(ctx, lane, 10)
	require.NoError(t, err)
	require.Empty(t, again, "a claimed row is leased")

	require.NoError(t, h.Store.Retry(ctx, ids[0], time.Now().Add(-time.Minute), "boom"))
	require.NoError(t, h.Store.Retry(ctx, ids[1], time.Now().Add(time.Hour), "boom"))

	due, err := h.Store.Claim(ctx, lane, 10)
	require.NoError(t, err)
	require.Len(t, due, 1)
	require.Equal(t, ids[0], due[0].ID)
	require.Equal(t, 2, due[0].Attempts)
}

// sentAndParkedAreNotClaimed finalizes two unclaimed due rows and claims.
func (h Harness) sentAndParkedAreNotClaimed(t *testing.T) {
	ctx, lane := context.Background(), newLane()
	ids := seed(t, h, lane, 3, time.Now().Add(-time.Hour))

	require.NoError(t, h.Store.MarkSent(ctx, ids[0]))
	require.NoError(t, h.Store.Park(ctx, ids[1], "poison"))

	recs, err := h.Store.Claim(ctx, lane, 10)
	require.NoError(t, err)
	require.Len(t, recs, 1)
	require.Equal(t, ids[2], recs[0].ID)
}

// statsCountsPendingAndParked checks Stats before and after finalizing.
func (h Harness) statsCountsPendingAndParked(t *testing.T) {
	ctx, lane := context.Background(), newLane()
	oldest := time.Now().Add(-time.Hour).Truncate(time.Second)
	ids := seed(t, h, lane, 3, oldest)

	stats, err := h.Store.Stats(ctx, lane)
	require.NoError(t, err)
	require.Equal(t, 3, stats.Pending)
	require.Equal(t, 0, stats.Parked)
	require.True(
		t,
		oldest.Equal(stats.OldestPending),
		"oldest %v, got %v",
		oldest,
		stats.OldestPending,
	)

	require.NoError(t, h.Store.MarkSent(ctx, ids[0]))
	require.NoError(t, h.Store.Park(ctx, ids[1], "poison"))

	stats, err = h.Store.Stats(ctx, lane)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Pending)
	require.Equal(t, 1, stats.Parked)
	require.True(t, oldest.Add(2*time.Second).Equal(stats.OldestPending))

	empty, err := h.Store.Stats(ctx, newLane())
	require.NoError(t, err)
	require.Equal(t, types.OutboxStats{}, empty)
}

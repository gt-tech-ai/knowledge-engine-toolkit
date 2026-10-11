package river

import (
	"testing"

	"github.com/riverqueue/river"
)

// TestBuildQueues tests the runtime's queue construction — the per-client concurrency
// config.
//
// Why this test is important:
//   - A worker's per-replica concurrency cap for a job kind IS its named queue's
//     MaxWorkers; this verifies a named queue is capped at exactly the requested K and
//     that the defaults apply. It is white-box because buildQueues is internal to
//     NewRuntime (which needs a live Postgres to open its pool).
//
// What it tests:
//   - A named queue + K maps to exactly {name: {MaxWorkers: K}}; an empty name falls back
//     to the default queue and a non-positive maxWorkers to the default cap.
func TestBuildQueues(t *testing.T) {
	t.Parallel()

	q := buildQueues("heavy", 4)
	if len(q) != 1 {
		t.Fatalf("want exactly 1 queue, got %d", len(q))
	}
	cfg, ok := q["heavy"]
	if !ok {
		t.Fatal("named queue heavy missing")
	}
	if cfg.MaxWorkers != 4 {
		t.Errorf("heavy MaxWorkers = %d, want 4", cfg.MaxWorkers)
	}

	def := buildQueues("", 0)
	cfg, ok = def[river.QueueDefault]
	if !ok {
		t.Fatal("default queue missing on empty name")
	}
	if cfg.MaxWorkers != defaultMaxWorkers {
		t.Errorf("default MaxWorkers = %d, want %d", cfg.MaxWorkers, defaultMaxWorkers)
	}
}

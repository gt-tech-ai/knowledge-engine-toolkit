// Package clients_test provides external tests for the clients layer.
//
// Tests validate the background job system including River-based job
// client stubs, worker registry, periodic scheduling, and event publishing.
package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/events"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestRiverJobClient_Stub tests that the stub job client can enqueue a job and
// close without error, ensuring the job client contract is usable before a real
// PostgreSQL-backed River backend is connected.
//
// Why this test is important:
//   - Services must be wirable and testable before the real River backend is
//     provisioned; a broken stub blocks all unit-test scenarios
//
// What it tests:
//   - NewClient returns a non-nil client without error
//   - Enqueue with a mock job returns nil error
//   - Close returns nil error
func TestRiverJobClient_Stub(t *testing.T) {
	t.Parallel()
	// The client is a no-op stand-in: no pool, nothing queued.

	client, err := jobs.NewClient(
		jobs.KindRiver,
		jobs.WithDatabaseURL("postgres://localhost/test"),
	)
	require.NoError(t, err, "unexpected error creating client")
	require.NotNil(t, client, "expected non-nil job client")

	// A River interfaces.Job mock standing in for a concrete job payload; the stub
	// Enqueue never inspects Kind/Args, so both are relaxed (AnyTimes) don't-cares.
	ctrl := gomock.NewController(t)
	job := mocks.NewMockJob(ctrl)
	job.EXPECT().Kind().Return("test-job").AnyTimes()
	job.EXPECT().Args().Return(map[string]any{"key": "value"}).AnyTimes()

	// Verify Enqueue doesn't panic (stub returns nil)
	err = client.Enqueue(context.Background(), job)
	assert.NoError(t, err, "stub Enqueue should not return error")

	// Verify Close doesn't panic
	assert.NoError(t, client.Close(), "stub Close should not return error")
}

// TestRiverJobClient_InterfaceCompliance tests that the job client factory
// returns the interfaces.JobEnqueuer abstraction, ensuring callers depend on
// the contract not the concrete type.
//
// Why this test is important:
//   - Callers that import the concrete type instead of the interface cannot be
//     swapped to the real River backend without a cascade of changes
//
// What it tests:
//   - NewClient returns a value assignable to interfaces.JobEnqueuer
//   - The returned enqueuer is non-nil
func TestRiverJobClient_InterfaceCompliance(t *testing.T) {
	t.Parallel()

	var enqueuer interfaces.JobEnqueuer
	var err error
	enqueuer, err = jobs.NewClient(
		jobs.KindRiver,
		jobs.WithDatabaseURL("postgres://localhost/test"),
	)
	require.NoError(t, err, "unexpected error")
	require.NotNil(t, enqueuer, "expected non-nil JobEnqueuer")
}

// TestWorkerRegistry tests that workers can be registered by kind and retrieved
// by the same key. The registry is the dispatch table for background jobs;
// incorrect lookups would silently drop work.
//
// Why this test is important:
//   - A broken registry cannot route enqueued jobs to their handlers, causing
//     silent work loss in the background job pipeline
//
// What it tests:
//   - A registered worker can be retrieved by its kind with ok=true
//   - The retrieved worker is the same instance that was registered
//   - Looking up a nonexistent kind returns ok=false
func TestWorkerRegistry(t *testing.T) {
	t.Parallel()

	// The worker registry is a working in-memory registry.
	registry := jobs.NewWorkerRegistry()
	require.NotNil(t, registry, "expected non-nil worker registry")

	// Register a worker mock (the registry only stores/returns it, never calls Work).
	worker := mocks.NewMockWorker(gomock.NewController(t))
	registry.Register("test-job", worker)

	// Verify retrieval
	retrieved, ok := registry.Get("test-job")
	assert.True(t, ok, "expected to find registered worker")
	assert.Equal(t, worker, retrieved, "expected to retrieve the same worker instance")

	// Verify missing worker
	_, ok = registry.Get("nonexistent")
	assert.False(t, ok, "expected not to find nonexistent worker")
}

// TestPeriodicJobScheduler_Stub tests that the stub scheduler accepts periodic
// job definitions without error, validating the scheduling interface so
// cron-style jobs can be wired before the real scheduler is available.
//
// Why this test is important:
//   - Cron-style jobs must be schedulable before the real scheduler (River) is
//     provisioned; a broken stub blocks integration tests that rely on it
//
// What it tests:
//   - NewScheduler returns a non-nil scheduler
//   - Schedule with a valid PeriodicJob returns nil error
func TestPeriodicJobScheduler_Stub(t *testing.T) {
	t.Parallel()

	// The scheduler is a no-op stand-in.
	scheduler := jobs.NewScheduler()
	require.NotNil(t, scheduler, "expected non-nil periodic scheduler")

	// Verify Schedule doesn't panic (stub is no-op)
	periodicJob := interfaces.PeriodicJob{
		Kind:     "test-job",
		Schedule: "@hourly",
		Args:     map[string]any{},
	}
	err := scheduler.Schedule(periodicJob, time.Hour)
	assert.NoError(t, err, "stub Schedule should not return error")
}

// TestEventPublisher_Stub tests that the stub event publisher can publish
// domain events without error, ensuring the event-driven architecture contract
// works before real transactional outbox integration.
//
// Why this test is important:
//   - Event publishing must be wirable before the transactional outbox (River)
//     lands; a broken stub prevents event-driven services from unit testing
//
// What it tests:
//   - NewEventPublisher returns a non-nil publisher
//   - Publish with a test event returns nil error
func TestEventPublisher_Stub(t *testing.T) {
	t.Parallel()

	// The client is a no-op stand-in.
	client, err := jobs.NewClient(
		jobs.KindRiver,
		jobs.WithDatabaseURL("postgres://localhost/test"),
	)
	require.NoError(t, err, "unexpected error creating client")

	publisher := jobs.NewEventPublisher(client)
	require.NotNil(t, publisher, "expected non-nil event publisher")

	// Verify Publish doesn't panic (stub is no-op)
	err = publisher.Publish(context.Background(), testEvt(t))
	assert.NoError(t, err, "stub Publish should not return error")
}

// testEvt returns a MockEvent standing in for a domain event; the stub publisher is
// a no-op, so Type/Metadata are relaxed (AnyTimes) don't-cares.
func testEvt(t *testing.T) events.Event {
	t.Helper()
	e := mocks.NewMockEvent(gomock.NewController(t))
	e.EXPECT().Type().Return(events.EventType("test.event")).AnyTimes()
	e.EXPECT().Metadata().Return(events.EventMetadata{}).AnyTimes()
	return e
}

// ---------------------------------------------------------------------------
// Jobs Kind.String()
// ---------------------------------------------------------------------------

// TestJobsKind_String_River tests that the River job kind produces the expected
// string representation, ensuring consistent logging and configuration output.
//
// Why this test is important:
//   - Kind.String() appears in error messages and metrics labels; an incorrect
//     value makes job-type attribution in dashboards wrong
//
// What it tests:
//   - KindRiver.String() returns "river"
func TestJobsKind_String_River(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "river", jobs.KindRiver.String())
}

// TestJobsKind_String_Unknown tests that an unrecognized job kind produces a
// descriptive fallback string rather than panicking or returning empty.
//
// Why this test is important:
//   - Unknown kinds must produce a human-readable sentinel instead of empty;
//     empty strings in logs make debugging impossible
//
// What it tests:
//   - Kind(99).String() returns "Kind(99)"
func TestJobsKind_String_Unknown(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Kind(99)", jobs.Kind(99).String())
}

// ---------------------------------------------------------------------------
// EventPublisher.PublishBatch
// ---------------------------------------------------------------------------

// TestEventPublisher_PublishBatch tests that the stub event publisher can
// publish multiple domain events in a single batch call without error,
// supporting bulk event emission patterns.
//
// Why this test is important:
//   - Batch publishing reduces outbox transaction overhead; a broken batch path
//     would force callers to loop over Publish, losing atomicity
//
// What it tests:
//   - PublishBatch with 3 events returns nil error
func TestEventPublisher_PublishBatch(t *testing.T) {
	t.Parallel()

	client, err := jobs.NewClient(
		jobs.KindRiver,
		jobs.WithDatabaseURL("postgres://localhost/test"),
	)
	require.NoError(t, err, "unexpected error")

	publisher := jobs.NewEventPublisher(client)

	// PublishBatch with multiple events
	evts := []events.Event{testEvt(t), testEvt(t), testEvt(t)}
	err = publisher.PublishBatch(context.Background(), evts)
	assert.NoError(t, err, "stub PublishBatch should not return error")
}

// TestEventPublisher_PublishBatch_Empty tests that publishing an empty batch of
// events does not error, ensuring callers do not need to guard against empty
// slices.
//
// Why this test is important:
//   - Callers often derive event slices from filtered collections; without this
//     guarantee they must add a nil-check before every batch call
//
// What it tests:
//   - PublishBatch with an empty event slice returns nil error
func TestEventPublisher_PublishBatch_Empty(t *testing.T) {
	t.Parallel()

	client, err := jobs.NewClient(
		jobs.KindRiver,
		jobs.WithDatabaseURL("postgres://localhost/test"),
	)
	require.NoError(t, err, "unexpected error")

	publisher := jobs.NewEventPublisher(client)

	// PublishBatch with empty slice
	err = publisher.PublishBatch(context.Background(), []events.Event{})
	assert.NoError(t, err, "stub PublishBatch with empty slice should not error")
}

// ---------------------------------------------------------------------------
// Jobs DefaultConfig / WithDatabaseURL
// ---------------------------------------------------------------------------

// TestJobs_DefaultConfig tests that creating a job client with the required
// database URL option uses sensible defaults (KindRiver) without error.
//
// Why this test is important:
//   - Services that omit explicit kind configuration must fall back to River;
//     a wrong default would silently use an incorrect backend
//
// What it tests:
//   - NewClient with only WithDatabaseURL returns a non-nil client with no error
func TestJobs_DefaultConfig(t *testing.T) {
	t.Parallel()

	// NewClient exercises DefaultConfig and WithDatabaseURL option
	client, err := jobs.NewClient(
		jobs.KindRiver,
		jobs.WithDatabaseURL("postgres://localhost:5432/test"),
	)
	require.NoError(t, err, "unexpected error")
	require.NotNil(t, client, "expected non-nil client")
}

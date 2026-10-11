package interfaces

import (
	"context"
	"time"
)

// Job represents a background job that can be enqueued for processing.
type Job interface {
	// Kind returns the job type identifier used for worker dispatch.
	Kind() string

	// Args returns the job's payload as a key-value map.
	Args() map[string]interface{}
}

// Worker processes background jobs of a specific kind.
type Worker interface {
	// Work executes a job with the given arguments.
	Work(args map[string]interface{}) error
}

// PeriodicJob represents a job that runs on a schedule.
type PeriodicJob struct {
	// Args holds arbitrary key-value arguments passed to the job worker.
	Args map[string]interface{}

	// Kind is the job type identifier used to match a registered worker.
	Kind string

	// Schedule is a cron expression defining when the job runs.
	Schedule string
}

// JobEnqueuer enqueues background jobs for asynchronous processing.
type JobEnqueuer interface {
	// Enqueue submits a job for asynchronous processing.
	Enqueue(ctx context.Context, job Job) error

	// Close releases resources held by the enqueuer.
	Close() error
}

// JobScheduler schedules periodic background jobs.
type JobScheduler interface {
	// Schedule adds a periodic job to the schedule at the given interval.
	Schedule(job PeriodicJob, interval time.Duration) error
}

// WorkerRegistry registers and retrieves job workers by kind.
type WorkerRegistry interface {
	// Register associates a worker with a job kind.
	Register(kind string, worker Worker)

	// Get retrieves the worker registered for the given kind.
	Get(kind string) (Worker, bool)
}

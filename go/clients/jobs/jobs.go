// Package jobs provides a factory for background job queue implementations
// with multiple backends.
//
// Use NewFromConfig() for app wiring (the tier-root factory), or the lower-level
// NewClient(), NewWorkerRegistry(), NewScheduler(), or NewEventPublisher()
// constructors. The factory selects the implementation at runtime based on Kind;
// the River backend lives in the river/ subpackage.
//
// Example:
//
//	client, err := jobs.NewClient(jobs.KindRiver, jobs.WithDatabaseURL(dsn))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer client.Close()
package jobs

import (
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/jobs/river"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

// Kind specifies which job queue implementation to use.
type Kind int

const (
	// KindRiver uses River for PostgreSQL-backed job processing. Suitable
	// for deployments with a PostgreSQL database.
	KindRiver Kind = iota
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindRiver:
		return "river"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// NewFromConfig builds a JobEnqueuer from the jobs Config, selecting the backend
// by cfg.Kind. It is the app-wiring entrypoint mirroring cache/storage
// NewFromConfig — the factory sits at the tier root above the river/ backend.
func NewFromConfig(cfg Config) (interfaces.JobEnqueuer, error) {
	switch cfg.Kind {
	case KindRiver:
		return river.NewClient(river.Config{
			DatabaseURL: cfg.River.DatabaseURL,
		})

	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown jobs kind: %v", cfg.Kind),
		)
	}
}

// NewClient creates a job client of the specified kind with optional
// functional options. It is a thin option-based wrapper over NewFromConfig.
// Returns an error if the kind is unknown.
func NewClient(
	kind Kind,
	opts ...options.Option[Config],
) (interfaces.JobEnqueuer, error) {
	cfg := DefaultConfig()
	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)
	return NewFromConfig(cfg)
}

// NewWorkerRegistry creates a new worker registry. The registry is
// backend-agnostic and always returns a River WorkerRegistry.
func NewWorkerRegistry() interfaces.WorkerRegistry {
	return river.NewWorkerRegistry()
}

// NewScheduler creates a new periodic job scheduler. The scheduler is
// backend-agnostic and always returns a River Scheduler.
func NewScheduler() interfaces.JobScheduler {
	return river.NewScheduler()
}

// NewEventPublisher creates a new event publisher backed by the given enqueuer.
func NewEventPublisher(enqueuer interfaces.JobEnqueuer) interfaces.EventPublisher {
	return river.NewEventPublisher(enqueuer)
}

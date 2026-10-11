package river

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertion.
var _ interfaces.JobScheduler = (*Scheduler)(nil)

// Scheduler is a no-op JobScheduler. Periodic jobs run through River via
// RuntimeConfig.PeriodicJobs instead.
type Scheduler struct{}

// NewScheduler creates a new periodic job scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{}
}

// Schedule accepts job without scheduling it and returns nil.
func (s *Scheduler) Schedule(_ interfaces.PeriodicJob, _ time.Duration) error {
	return nil
}

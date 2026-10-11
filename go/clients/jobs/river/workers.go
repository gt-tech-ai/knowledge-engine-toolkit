package river

import (
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertion.
var _ interfaces.WorkerRegistry = (*WorkerRegistry)(nil)

// WorkerRegistry is an in-memory registry of job workers keyed by job kind, safe
// for concurrent use. River's own worker registration goes through
// RuntimeConfig.Workers.
type WorkerRegistry struct {
	// workers maps job kind strings to their Worker implementations.
	workers map[string]interfaces.Worker

	// mu guards concurrent access to the workers map.
	mu sync.RWMutex
}

// NewWorkerRegistry creates a new worker registry.
func NewWorkerRegistry() *WorkerRegistry {
	return &WorkerRegistry{
		workers: make(map[string]interfaces.Worker),
	}
}

// Register registers a worker for a job kind.
func (r *WorkerRegistry) Register(kind string, worker interfaces.Worker) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workers[kind] = worker
}

// Get retrieves a worker by job kind.
func (r *WorkerRegistry) Get(kind string) (interfaces.Worker, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[kind]
	return w, ok
}

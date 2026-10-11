// Package services provides the shared service base and a generic operation
// decorator/builder for CLI (operation) services. It is the operation-oriented
// counterpart to go/services/service (entity CRUD): Base carries the
// dependencies every CLI service shares, and Build assembles a decorated
// interfaces.OpService[A,R] from a single run function.
package services

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"

// Base carries the dependencies common to every CLI service: a name (used as a
// label in logs, metrics, and decorator actions) and a structured logger that
// operations use to report progress. Idiosyncratic services embed *Base and
// read these via the accessors instead of threading them through every call.
type Base struct {
	// log is the structured logger an operation uses to report progress.
	log interfaces.Logger

	// name labels the service in logs, metrics, and decorator actions.
	name string
}

// NewBase constructs a Base with the given name and logger.
func NewBase(name string, log interfaces.Logger) *Base {
	return &Base{name: name, log: log}
}

// Name returns the service name.
func (b *Base) Name() string { return b.name }

// Log returns the structured logger.
func (b *Base) Log() interfaces.Logger { return b.log }

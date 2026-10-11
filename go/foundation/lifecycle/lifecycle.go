// Package lifecycle provides a Manager that starts a set of lifecycle-managed
// clients in registration order and stops them in reverse for graceful shutdown,
// so a composition root wires its clients once and coordinates their startup and
// teardown centrally instead of by hand.
package lifecycle

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// NoOp is an embeddable no-op Lifecycle for stateless clients that hold no
// persistent connection (SDK clients whose every call is an independent request):
// Start and Stop do nothing. Embed it so such a client satisfies interfaces.Lifecycle
// without re-declaring the identical no-op methods on each backend.
type NoOp struct{}

// Start is a no-op: a stateless client has no resource to open.
func (NoOp) Start(context.Context) error { return nil }

// Stop is a no-op: a stateless client has no resource to close.
func (NoOp) Stop(context.Context) error { return nil }

// entry pairs a lifecycle-managed client with the name used in start/stop errors.
type entry struct {
	// client is the lifecycle-managed client to start and stop.
	client interfaces.Lifecycle
	// name identifies the client in start/stop errors.
	name string
}

// Manager starts registered clients in registration order and stops them in
// reverse order, so dependencies come up before dependents and shut down after
// them. It is not safe for concurrent Register during Start/Stop; register at the
// composition root, then run.
type Manager struct {
	// entries are the registered clients in registration (start) order.
	entries []entry
}

// New returns an empty lifecycle Manager.
func New() *Manager { return &Manager{} }

// Register adds a lifecycle-managed client under a name. Registration order is the
// start order; the reverse is the stop order.
func (m *Manager) Register(name string, client interfaces.Lifecycle) {
	m.entries = append(m.entries, entry{client: client, name: name})
}

// Start starts every registered client in order. If one fails, the clients already
// started are stopped (rolled back) in reverse before the error is returned.
func (m *Manager) Start(ctx context.Context) error {
	for i, e := range m.entries {
		if err := e.client.Start(ctx); err != nil {
			for j := i - 1; j >= 0; j-- {
				_ = m.entries[j].client.Stop(ctx)
			}
			return apperr.Wrap(err, apperr.CodeInternal, "start "+e.name)
		}
	}
	return nil
}

// Stop stops every registered client in reverse order, continuing past failures
// and returning the first error encountered so shutdown always completes.
func (m *Manager) Stop(ctx context.Context) error {
	var firstErr error
	for i := len(m.entries) - 1; i >= 0; i-- {
		if err := m.entries[i].client.Stop(ctx); err != nil && firstErr == nil {
			firstErr = apperr.Wrap(
				err,
				apperr.CodeInternal,
				"stop "+m.entries[i].name,
			)
		}
	}
	return firstErr
}

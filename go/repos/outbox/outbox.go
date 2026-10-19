// Package outbox is the repository tier of the transactional outbox: the relay's seam
// over an outbox store (stores/outbox).
package outbox

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
)

// New returns store on the repository tier, decorated from decs as the repository named
// name. Claim runs without retry: it leases rows and counts an attempt, so a second
// attempt would lease another batch. The finalizers (guarded on the row still pending),
// Stats and DeleteSentBefore are idempotent and run through the full chain. A decoration
// that fails to resolve is an error.
func New(
	decs decorate.Decorations,
	name string,
	store interfaces.OutboxStore,
) (interfaces.OutboxRepository, error) {
	b, err := repository.NewBase[interfaces.OutboxRepository](decs, name)
	if err != nil {
		return nil, err
	}
	return decorate.NewOutbox(b.Once(), b.Subject, store), nil
}

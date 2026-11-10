package role

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// ResourceOps are the Subjects a decorated Resource runs through: Create's, apart since
// a create is not idempotent and so is never retried, and every other role's.
type ResourceOps struct {
	// Create runs Create.
	Create decorate.Subject

	// Rest runs every role but Create.
	Rest decorate.Subject
}

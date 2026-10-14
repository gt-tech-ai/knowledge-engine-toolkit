package fixtures

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// Workflows returns an undecorated (recovery-only), unbounded workflow.Base whose Tx
// workflows open their transactions through tx (nil when the test builds no Tx
// workflow), for tests that build a consumer's request workflows.
func Workflows(tx interfaces.TransactionManager) workflow.Base {
	return workflow.NewBase(tx, decorate.Deps{})
}

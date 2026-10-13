package fixtures

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	workflowscfg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/workflows"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// Workflows returns an unobserved, unbounded workflow.Base whose Tx workflows open
// their transactions through tx (nil when the test builds only Read and Wrap
// workflows), for tests that build a consumer's request workflows.
func Workflows(tx interfaces.TransactionManager) workflow.Base {
	return workflow.NewBase(tx, decorate.Observability{}, workflowscfg.Config{})
}

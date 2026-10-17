package fixtures

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// Workflows returns an undecorated (recovery-only), unbounded workflow.Base whose Tx
// workflows open their transactions through tx (nil when the test builds no Tx
// workflow), for tests that build a consumer's request workflows.
func Workflows(tx interfaces.TransactionManager) workflow.Base {
	return workflow.NewBase(tx, decorate.Deps{})
}

// Pipelines returns an undecorated (recovery-only) pipeline.Base, for tests that build
// a consumer's pipelines.
func Pipelines() pipeline.Base {
	return pipeline.NewBase(decorate.Deps{})
}

// Service returns an undecorated (recovery-only) service.Base named name, for tests
// that build a consumer's services.
func Service(name string) service.Base {
	return service.NewBase(name, decorate.Deps{})
}

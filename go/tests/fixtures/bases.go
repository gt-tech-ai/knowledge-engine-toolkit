package fixtures

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// Workflows returns an undecorated (recovery-only), unbounded workflow.Base named
// "test" whose commands and saga steps open their transactions through tx (nil when the
// test runs none), for tests that build a consumer's workflows.
func Workflows(tx interfaces.TransactionManager) workflow.Base {
	return workflow.NewBase("test", tx, decorate.Deps{})
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

// Must returns v, panicking when err is non-nil: a test's wiring of a constructor that
// returns (T, error), such as a workflow built from decorations, fails loudly.
func Must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

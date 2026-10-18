package pipeline

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Resource is the decorated pipeline over svc: every role runs as one operation of the
// pipeline, whose contract embeds interfaces.Resource.
func (b Base) Resource[T, P any, ID comparable, C, U any](
	svc interfaces.Resource[T, P, ID, C, U],
) interfaces.Resource[T, P, ID, C, U] {
	return decorate.NewResource[T, P, ID, C, U](
		decorate.ResourceOps{Create: b.Subject, Rest: b.Subject},
		svc,
		nil,
		nil,
	)
}

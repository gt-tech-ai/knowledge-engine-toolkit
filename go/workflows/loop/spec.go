package loop

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Spec declares one background loop: the pass it runs, the op that pass is decorated
// under, and its schedule. A service lists its loops as Specs and the composition root
// turns them into one Group with NewGroupFromSpecs, so the service never builds a
// Ticker or a decorator itself.
type Spec struct {
	// Run is one iteration of the loop.
	Run func(ctx context.Context) error

	// Name labels the loop in logs and in the Group.
	Name string

	// Op is the workflow op the pass is decorated under.
	Op string

	// Schedule paces the loop.
	Schedule Config
}

// Decorator wraps one loop pass in the composition root's workflow decorator stack under
// op, so each pass is logged, counted, traced and recovered like any other workflow.
type Decorator func(
	op string,
	pass interfaces.Workflow[struct{}, struct{}],
) interfaces.Workflow[struct{}, struct{}]

// NewGroupFromSpecs builds one Ticker per spec, its pass wrapped by decorate (a nil
// decorate leaves passes undecorated), into a Group that starts and stops them together,
// in spec order. An invalid schedule is CodeInvalidInput; a nil logger or Run panics, as
// a wiring bug.
func NewGroupFromSpecs(
	specs []Spec,
	decorate Decorator,
	logger interfaces.Logger,
) (*Group, error) {
	group := NewGroup(logger)
	for _, s := range specs {
		pass := Pass(s.Run)
		if decorate != nil {
			pass = decorate(s.Op, pass)
		}
		ticker, err := NewTicker(s.Name, pass, s.Schedule, logger)
		if err != nil {
			return nil, err
		}
		group.Add(s.Name, ticker)
	}
	return group, nil
}

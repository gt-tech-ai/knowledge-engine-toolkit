package suite

import (
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/gate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Composite composes a gate from a types.Composite: each phase's suites' jobs (Suites
// builds them per reference) then its named jobs (Jobs builds them), in the spec's order.
// A product's preflight is one Composite per level, so its phase order, the suites each
// level unions and the flags they run under are data.
type Composite struct {
	// Obs receives the gate's progress (may be nil).
	Obs interfaces.ExecutionObserver

	// Suites builds a suite reference's jobs: the product's suite gate under the
	// reference's flags, selection and strictness. A nil Suites makes every reference
	// one failing job.
	Suites func(ref types.SuiteRef) []interfaces.AnyJob

	// Jobs maps the names a phase's Jobs may carry to their builders; a builder
	// returning nil withholds its job (a phase the run's options skip). An unknown name
	// is one failing job, so a typo in the data fails the gate rather than dropping a
	// check.
	Jobs map[string]func() interfaces.AnyJob

	// Decs decorates the gate.
	Decs decorate.Decorations

	// Spec is the composite definition.
	Spec types.Composite
}

var _ gate.Factory = &Composite{}

// Gate builds the composite's gate: one phase per spec phase that builds a job, serial
// or parallel as the phase says.
func (c *Composite) Gate() *gate.Gate {
	b := gate.New(c.Spec.Title)
	if c.Obs != nil {
		b.Observe(c.Obs)
	}

	for _, p := range c.Spec.Phases {
		jobs := c.phase(p)
		if len(jobs) == 0 {
			continue
		}

		if p.Serial {
			b.Serial(p.Group, jobs...)
		} else {
			b.Parallel(p.Group, jobs...)
		}
	}

	return b.Decorate(c.Decs)
}

// phase builds one phase's jobs: the suites' then the named ones.
func (c *Composite) phase(p types.CompositePhase) []interfaces.AnyJob {
	return append(c.suiteJobs(p.Suites), c.namedJobs(p.Jobs)...)
}

// suiteJobs expands the phase's suite references, a failing job standing in when no
// suites are wired.
func (c *Composite) suiteJobs(refs []types.SuiteRef) []interfaces.AnyJob {
	var jobs []interfaces.AnyJob

	for _, ref := range refs {
		if c.Suites == nil {
			jobs = append(jobs, failed(ref.Name, apperr.New(apperr.CodeInvalidInput,
				fmt.Sprintf("%s: no suites for %q", c.Spec.Title, ref.Name))))

			continue
		}

		jobs = append(jobs, c.Suites(ref)...)
	}

	return jobs
}

// namedJobs builds the phase's product jobs, a failing job standing in for an unknown
// name; a builder may decline with nil.
func (c *Composite) namedJobs(names []string) []interfaces.AnyJob {
	var jobs []interfaces.AnyJob

	for _, name := range names {
		build, ok := c.Jobs[name]
		if !ok {
			jobs = append(jobs, failed(name, apperr.New(apperr.CodeInvalidInput,
				fmt.Sprintf("%s: no job %q", c.Spec.Title, name))))

			continue
		}

		if j := build(); j != nil {
			jobs = append(jobs, j)
		}
	}

	return jobs
}

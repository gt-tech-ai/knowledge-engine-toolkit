package decorate

import (
	"context"
	"maps"
	"slices"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
)

// Scope resolves the decoration of one component's operations, each under its own name
// beneath the component's: the operation "count" of the repository "api.docs" is the
// repository-tier component "api.docs.count", so the config can tune one operation, or
// every operation of the component through its own key. A Scope built by NewScope gives
// every operation the same Deps. The zero Scope runs operations undecorated.
type Scope struct {
	// decs resolves each operation's Deps; nil gives every operation deps.
	decs *Decorations

	// name is the component's name, the prefix of each operation's.
	name string

	// deps backs every operation when decs is nil.
	deps Deps

	// tier is the tier the operations run in.
	tier Tier
}

// NewScope returns the Scope of the component named name in tier whose operations all
// share deps.
func NewScope(tier Tier, name string, deps Deps) Scope {
	return Scope{tier: tier, name: name, deps: deps}
}

// Scope returns the Scope of the component named name in tier, whose operations each
// take their own decoration from d.
func (d Decorations) Scope(tier Tier, name string) Scope {
	return Scope{decs: &d, tier: tier, name: name}
}

// Name returns the component's name.
func (s Scope) Name() string { return s.name }

// Subject returns the Subject of the operation op, named name+"."+op, with each adjust
// applied to its Deps (Deps.WithoutRetry for a write that must not replay). Resolved from
// Decorations, a resolution error is recorded for Verify and the operation runs with
// recovery only until boot fails on it.
func (s Scope) Subject(op string, adjust ...func(Deps) Deps) Subject {
	name := op
	if s.name != "" {
		name = s.name + "." + op
	}
	deps := s.deps
	if s.decs != nil {
		var err error
		if deps, err = s.decs.Deps(s.tier, name); err != nil {
			s.decs.reg.fail(err)
			deps = Deps{}
		}
	}
	for _, a := range adjust {
		deps = a(deps)
	}
	return s.tier.Subject(name, deps)
}

// OpFunc is one operation: a function that is an interfaces.Op.
type OpFunc[In, Out any] func(context.Context, In) (Out, error)

// Compile-time interface compliance check.
var _ interfaces.Op[string, string] = OpFunc[string, string](nil)

// Execute runs the operation.
func (f OpFunc[In, Out]) Execute(ctx context.Context, in In) (Out, error) {
	return f(ctx, in)
}

// Operation is fn run as subject's "Execute" operation. A nil fn panics, as a wiring
// bug.
func Operation[In, Out any](
	subject Subject,
	fn func(context.Context, In) (Out, error),
) OpFunc[In, Out] {
	if fn == nil {
		panic("decorate: operation " + subject.Name() + " has a nil function")
	}
	return func(ctx context.Context, in In) (Out, error) {
		return subject.Exec(ctx, "Execute", func(ctx context.Context) (Out, error) {
			return fn(ctx, in)
		})
	}
}

// Component is one component Decorations resolved: its tier and its name.
type Component struct {
	// Tier is the tier's name ("repository", "client", …).
	Tier string

	// Name is the component's dotted name.
	Name string
}

// Components returns every component d resolved, ordered by tier then name: the
// inventory a generator writes one decoration entry for. The zero Decorations has none.
func (d Decorations) Components() []Component {
	if d.reg == nil {
		return nil
	}
	d.reg.mu.Lock()
	defer d.reg.mu.Unlock()
	var out []Component
	for _, tier := range slices.Sorted(maps.Keys(d.reg.used)) {
		for _, name := range slices.Sorted(maps.Keys(d.reg.used[tier])) {
			out = append(out, Component{Tier: tier, Name: name})
		}
	}
	return out
}

// VerifyExplicit is Verify, and also fails CodeInvalidInput naming every resolved
// component under prefixes that has no override of its own name: in a deployable whose
// decoration file is generated with one entry per component, a component without one was
// added after the file was generated, and takes only its tier's defaults.
func (d Decorations) VerifyExplicit(prefixes ...string) error {
	err := d.Verify(prefixes...)
	if d.reg == nil {
		return err
	}
	cfg := d.cfg
	if cfg == nil {
		cfg = &decoration.Config{}
	}
	keys := map[string]string{}
	overrides := map[string]map[string]decoration.Layers{}
	sections := cfg.Sections()
	for i := range sections {
		keys[sections[i].Name] = sections[i].Key
		overrides[sections[i].Name] = sections[i].Tier.Overrides
	}
	var missing []string
	for _, c := range d.Components() {
		if !owned(c.Name, prefixes) {
			continue
		}
		if _, ok := overrides[c.Tier][c.Name]; !ok {
			missing = append(
				missing,
				decoration.SectionKey+"."+keys[c.Tier]+".overrides."+c.Name,
			)
		}
	}
	if len(missing) == 0 {
		return err
	}
	miss := apperr.New(
		apperr.CodeInvalidInput,
		"no entry for "+strings.Join(missing, ", "),
	)
	if err != nil {
		return apperr.Wrap(apperr.Join(err, miss), apperr.CodeInvalidInput, "decoration")
	}
	return apperr.Wrap(miss, apperr.CodeInvalidInput, "decoration")
}

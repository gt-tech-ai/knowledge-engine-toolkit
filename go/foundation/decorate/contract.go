package decorate

import (
	"reflect"
	"strings"
	"unicode"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// NewSubject returns the Subject of the component named name in tier whose operations
// are ops: the component resolves as name, and each operation as name+"."+op, so the
// config can tune one operation apart from the rest of its component. Every name is
// recorded for Verify. An operation reuses its component's circuit breaker, bulkhead and
// rate limiter unless its own override configures that layer, so a limit set on the
// component bounds all of its operations together. Each adjust runs, in order, on every
// resolved Deps (a consumer's handler drops its retry and breaker). Nil ops builds a
// Subject that runs any operation on the component's chains, for a component whose
// operations are wire-level names known only per call (an RPC procedure, an HTTP route).
// A resolution error is CodeInvalidInput.
func (d Decorations) NewSubject(
	tier Tier,
	name string,
	ops []string,
	adjust ...func(Deps) Deps,
) (Subject, error) {
	comp, err := d.Deps(tier, name)
	if err != nil {
		return Subject{}, err
	}
	comp = adjusted(comp, adjust)
	s := Subject{name: name, all: tier.chains(name, comp)}
	if ops == nil {
		return s, nil
	}
	s.ops = make(map[string]chains, len(ops))
	for _, op := range ops {
		key := name + "." + op
		deps, err := d.Deps(tier, key)
		if err != nil {
			return Subject{}, err
		}
		deps = adjusted(d.shareStateful(tier, key, deps, comp), adjust)
		s.ops[op] = tier.chains(name, deps)
	}
	return s, nil
}

// SubjectOf returns the Subject of the component named name in tier whose operations are
// the methods of its contract C, an interface: NewSubject over OpsOf[C].
func (d Decorations) SubjectOf[C any](
	tier Tier,
	name string,
	adjust ...func(Deps) Deps,
) (Subject, error) {
	ops, err := OpsOf[C]()
	if err != nil {
		return Subject{}, err
	}
	return d.NewSubject(tier, name, ops, adjust...)
}

// adjusted returns d after each adjust, in order.
func adjusted(d Deps, adjust []func(Deps) Deps) Deps {
	for _, a := range adjust {
		d = a(d)
	}
	return d
}

// shareStateful returns op's deps with its component's breaker, bulkhead and rate
// limiter in place of its own, for each of those layers op's own override (key) does not
// configure.
func (d Decorations) shareStateful(tier Tier, key string, op, comp Deps) Deps {
	own := d.ownOverride(tier, key)
	if own.CircuitBreaker.Enabled == nil {
		op.Breaker = comp.Breaker
	}
	if own.Bulkhead.Enabled == nil {
		op.Bulkhead, op.ShedLoad = comp.Bulkhead, comp.ShedLoad
	}
	if own.RateLimit.Enabled == nil {
		op.RateLimiter = comp.RateLimiter
	}
	return op
}

// OpsOf returns the operations of the contract C: its methods' names in snake case. C
// that is not an interface, or has no methods, is CodeInternal.
func OpsOf[C any]() ([]string, error) {
	t := reflect.TypeFor[C]()
	if t.Kind() != reflect.Interface || t.NumMethod() == 0 {
		return nil, apperr.New(
			apperr.CodeInternal,
			"decorate: contract "+t.String()+" is not an interface with methods",
		)
	}
	ops := make([]string, t.NumMethod())
	for i := range ops {
		ops[i] = OpName(t.Method(i).Name)
	}
	return ops, nil
}

// OpName returns the operation name of the method named method: its name in snake case,
// an acronym kept as one word (FindByKey → find_by_key, GetURL → get_url,
// HTTPStatus → http_status).
func OpName(method string) string {
	r := []rune(method)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) && i > 0 {
			prevLower := unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1])
			nextLower := i+1 < len(r) && unicode.IsLower(r[i+1])
			if prevLower || (unicode.IsUpper(r[i-1]) && nextLower) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}

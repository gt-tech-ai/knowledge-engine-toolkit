package decorate

import (
	"reflect"
	"strings"
	"unicode"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	comp = adjusted(&comp, adjust)

	subject := Subject{name: name, all: tier.chains(name, &comp), ops: nil, view: 0}

	if ops == nil {
		return subject, nil
	}

	subject.ops = make(map[string]chains, len(ops))
	for _, operation := range ops {
		key := name + "." + operation

		deps, err := d.Deps(tier, key)
		if err != nil {
			return Subject{}, err
		}

		deps = adjusted(new(d.shareStateful(tier, key, &deps, &comp)), adjust)
		subject.ops[operation] = tier.chains(name, &deps)
	}

	return subject, nil
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
func adjusted(dRef *Deps, adjust []func(Deps) Deps) Deps {
	deps := *dRef

	for _, a := range adjust {
		deps = a(deps)
	}

	return deps
}

// shareStateful returns op's deps with its component's breaker, bulkhead and rate
// limiter in place of its own, for each of those layers op's own override (key) does not
// configure.
func (d Decorations) shareStateful(tier Tier, key string, opRef, comp *Deps) Deps {
	operation := *opRef

	own := d.ownOverride(tier, key)
	if own.CircuitBreaker.Enabled == nil {
		operation.Breaker = comp.Breaker
	}

	if own.Bulkhead.Enabled == nil {
		operation.Bulkhead, operation.ShedLoad = comp.Bulkhead, comp.ShedLoad
	}

	if own.RateLimit.Enabled == nil {
		operation.RateLimiter = comp.RateLimiter
	}

	return operation
}

// OpsOf returns the operations of the contract C: its methods' names in snake case. C
// that is not an interface, or has no methods, is CodeInternal.
func OpsOf[C any]() ([]string, error) {
	kind := reflect.TypeFor[C]()
	if kind.Kind() != reflect.Interface || kind.NumMethod() == 0 {
		return nil, apperr.New(
			apperr.CodeInternal,
			"decorate: contract "+kind.String()+" is not an interface with methods",
		)
	}

	ops := make([]string, kind.NumMethod())
	for i := range ops {
		ops[i] = OpName(kind.Method(i).Name)
	}

	return ops, nil
}

// OpName returns the operation name of the method named method: its name in snake case,
// an acronym kept as one word (FindByKey → find_by_key, GetURL → get_url,
// HTTPStatus → http_status).
func OpName(method string) string {
	r := []rune(method)

	var builder strings.Builder

	for i, c := range r {
		if startsWord(r, i) {
			_ = builder.WriteByte('_')
		}

		_, _ = builder.WriteRune(unicode.ToLower(c))
	}

	return builder.String()
}

// startsWord reports whether rune i begins a new snake_case word: an upper-case rune
// after a lower-case or digit, or the last capital of an acronym before a lower-case.
func startsWord(items []rune, index int) bool {
	if index == 0 || !unicode.IsUpper(items[index]) {
		return false
	}

	prev := items[index-1]
	nextLower := index+1 < len(items) && unicode.IsLower(items[index+1])

	return unicode.IsLower(prev) || unicode.IsDigit(prev) ||
		(unicode.IsUpper(prev) && nextLower)
}

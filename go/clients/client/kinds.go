package client

import (
	"context"
	"fmt"
	"slices"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Kind names one backend of a client family, as written in config.
type Kind string

// KindStub is the zero-infrastructure backend a family ships so its graph boots
// without I/O.
const KindStub Kind = "stub"

// Backend builds one Kind of a family from its Config C and Deps D into its interface T;
// ctx bounds any I/O the backend does to construct.
type Backend[C, D, T any] = func(ctx context.Context, cfg C, deps D) (T, error)

// Kinds is a client family's backend table: Family labels its errors and names the
// client in the tier, Default is the backend an empty Kind selects, Backends builds
// each Kind from the family's Config C and Deps D into its interface T, and Decorate
// runs a built backend through the client tier.
type Kinds[C, D, T any] struct {
	// Backends maps each Kind to its constructor.
	Backends map[Kind]Backend[C, D, T]

	// Decorate wraps a backend in the client tier as the client named name, its Base
	// built from decs with opts (client.NewBase).
	Decorate func(base T, decs decorate.Decorations, name string, opts ...Option) (T, error)

	// Options shape the family's Base (Producer for a publisher).
	Options []Option

	// Family names the client family ("cache", "lock", …).
	Family string

	// Default is the Kind an empty config selects.
	Default Kind

	// AtMostOnce lists the kinds whose operations are at-most-once (a Redis PUBLISH): a
	// retry could repeat a delivery, so Build never retries them whatever the config says.
	AtMostOnce []Kind
}

// New builds the backend kind names (Default when empty); ctx bounds any I/O the
// backend does to construct (an SDK config load). An unknown kind is
// CodeInvalidInput naming the family's known kinds.
func (k Kinds[C, D, T]) New(ctx context.Context, kind Kind, cfg C, deps D) (T, error) {
	if kind == "" {
		kind = k.Default
	}
	build, ok := k.Backends[kind]
	if !ok {
		var zero T
		return zero, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"%s: unknown kind %q (want one of %s)", k.Family, kind, k.known(),
		))
	}
	return build(ctx, cfg, deps)
}

// Build builds the backend kind names and runs it through the client tier as the client
// named name (Family when empty), one chain per method of T from decs; an AtMostOnce
// kind gets no retry.
func (k Kinds[C, D, T]) Build(
	ctx context.Context,
	kind Kind,
	cfg C,
	deps D,
	decs decorate.Decorations,
	name string,
) (T, error) {
	var zero T
	base, err := k.New(ctx, kind, cfg, deps)
	if err != nil {
		return zero, err
	}
	if name == "" {
		name = k.Family
	}
	if kind == "" {
		kind = k.Default
	}
	opts := slices.Clone(k.Options)
	if slices.Contains(k.AtMostOnce, kind) {
		opts = append(opts, AtMostOnce())
	}
	return k.Decorate(base, decs, name, opts...)
}

// known lists the family's kinds, sorted.
func (k Kinds[C, D, T]) known() string {
	names := make([]string, 0, len(k.Backends))
	for kind := range k.Backends {
		names = append(names, string(kind))
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

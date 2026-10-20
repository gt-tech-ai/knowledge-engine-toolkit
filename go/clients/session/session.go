// Package session resolves a credential from a secrets Source, verifies it once, and
// projects it into the environment a command runs with: the session a CLI command
// opens before it calls a cloud or an identity provider.
package session

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Provider resolves, verifies and caches credentials of type Cred.
type Provider[Cred any] struct {
	// src holds the raw credentials.
	src interfaces.Source

	// parse turns a raw secret into a credential.
	parse func([]byte) (Cred, error)

	// verify proves a credential works (a caller-identity call, a token exchange).
	verify func(context.Context, Cred) error

	// project renders a credential as `KEY=value` environment entries.
	project func(Cred) []string

	// cache holds verified credentials by reference.
	cache map[types.Ref]Cred

	// group collapses concurrent resolutions of one reference into one.
	group singleflight.Group

	// mu guards cache.
	mu sync.Mutex
}

// NewProvider returns a Provider over src. It does no I/O.
func NewProvider[Cred any](
	src interfaces.Source,
	parse func([]byte) (Cred, error),
	verify func(context.Context, Cred) error,
	project func(Cred) []string,
) *Provider[Cred] {
	return &Provider[Cred]{
		src: src, parse: parse, verify: verify, project: project,
		cache: map[types.Ref]Cred{},
	}
}

// Resolve returns the verified credential ref names, resolving and verifying it on
// first use and serving it from the cache after. A credential that fails verification
// is CodeUnauthorized and is not cached, so a fixed credential works on the next call.
func (p *Provider[Cred]) Resolve(ctx context.Context, ref types.Ref) (Cred, error) {
	p.mu.Lock()
	cred, ok := p.cache[ref]
	p.mu.Unlock()
	if ok {
		return cred, nil
	}
	v, err, _ := p.group.Do(ref.String(), func() (any, error) {
		return p.resolve(ctx, ref)
	})
	if err != nil {
		var zero Cred
		return zero, err
	}
	cred, _ = v.(Cred)
	return cred, nil
}

// resolve reads, parses and verifies ref's credential, then caches it.
func (p *Provider[Cred]) resolve(ctx context.Context, ref types.Ref) (Cred, error) {
	var zero Cred
	secret, err := p.src.Get(ctx, ref)
	if err != nil {
		return zero, err
	}
	cred, err := p.parse([]byte(secret.Reveal()))
	if err != nil {
		return zero, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"parse credential "+ref.String(),
		)
	}
	if err := p.verify(ctx, cred); err != nil {
		return zero, apperr.Wrap(err, apperr.CodeUnauthorized,
			"credential "+ref.String()+" failed verification")
	}
	p.mu.Lock()
	p.cache[ref] = cred
	p.mu.Unlock()
	return cred, nil
}

// Env returns ref's verified credential as environment entries.
func (p *Provider[Cred]) Env(ctx context.Context, ref types.Ref) ([]string, error) {
	cred, err := p.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	return p.project(cred), nil
}

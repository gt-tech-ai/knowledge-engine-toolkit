package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	coreprincipal "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/principal"
)

// TestPrincipal_CarriesTheConsumerPrincipalPerType tests the request-principal context
// carrier.
//
// Why this test is important:
//   - Every tier reads the caller through this carrier (the transport interceptor stores
//     it, services and repositories read it), so it must live in core; a principal of one
//     type leaking into a read of another type would authorize a caller under the wrong
//     model.
//
// What it tests:
//   - A stored principal reads back; principals of different types coexist without seeing
//     each other; an empty or nil context reports none.
func TestPrincipal_CarriesTheConsumerPrincipalPerType(t *testing.T) {
	t.Parallel()
	type user struct{ Sub string }
	type service struct{ ID string }

	ctx := coreprincipal.WithPrincipal(context.Background(), user{Sub: "u1"})
	ctx = coreprincipal.WithPrincipal(ctx, service{ID: "s1"})
	u, ok := coreprincipal.PrincipalFrom[user](ctx)
	assert.True(t, ok)
	assert.Equal(t, "u1", u.Sub)
	s, ok := coreprincipal.PrincipalFrom[service](ctx)
	assert.True(t, ok)
	assert.Equal(t, "s1", s.ID)

	_, ok = coreprincipal.PrincipalFrom[user](context.Background())
	assert.False(t, ok, "no principal stored")
	//nolint:staticcheck // a nil context must read as no principal, not panic
	_, ok = coreprincipal.PrincipalFrom[user](nil)
	assert.False(t, ok, "nil context")
}

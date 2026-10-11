package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
)

// The BaseRepository provides the full CRUD surface, so it must satisfy every
// role interface individually — the compile-time proof that the roles' method
// signatures byte-match the methods the base already implements.
var (
	_ interfaces.Reader[any, string] = (*repository.BaseRepository[any, any, string])(nil)
	_ interfaces.Lister[any, any]    = (*repository.BaseRepository[any, any, string])(nil)
	_ interfaces.Writer[any, string] = (*repository.BaseRepository[any, any, string])(nil)
	_ interfaces.Deleter[string]     = (*repository.BaseRepository[any, any, string])(nil)
	_ interfaces.Exister[string]     = (*repository.BaseRepository[any, any, string])(nil)
)

// The BaseService has no Exists, so it satisfies every role except Exister.
var (
	_ interfaces.Reader[any, string] = (*service.BaseService[any, any, string])(nil)
	_ interfaces.Lister[any, any]    = (*service.BaseService[any, any, string])(nil)
	_ interfaces.Writer[any, string] = (*service.BaseService[any, any, string])(nil)
	_ interfaces.Deleter[string]     = (*service.BaseService[any, any, string])(nil)
)

// roleProbeEntity + roleProbeParams are the minimal type args for a probe store
// that exercises the role interfaces without any production dependency.
type (
	roleProbeEntity struct{ ID string }
	roleProbeParams struct{}
)

// readOnlyProbe implements ONLY List — the shape enables: a resource
// that embeds the single role it honors instead of the full Store (and panicking
// the rest).
type readOnlyProbe struct{}

// List returns an empty page; the behavior is irrelevant — the test asserts only
// which role interfaces this type does and does not satisfy.
func (readOnlyProbe) List(
	_ context.Context,
	_ roleProbeParams,
	_ types.PageRequest,
) (*types.Page[roleProbeEntity], error) {
	return &types.Page[roleProbeEntity]{}, nil
}

// A List-only type satisfies Lister (compile-time) — the whole point of the split.
var _ interfaces.Lister[roleProbeEntity, roleProbeParams] = (*readOnlyProbe)(nil)

// The full-CRUD BaseRepository still satisfies the recomposed Store through the
// role composition — proof the alias is backward-compatible.
var _ interfaces.Store[any, any, string] = (*repository.BaseRepository[any, any, string])(
	nil,
)

// TestReadOnlyProbeIsListerNotStore tests that a type embedding only the Lister
// role satisfies interfaces.Lister but NOT the full interfaces.Store.
//
// Why this test is important:
//   - This is the capability exists to deliver: a read-only resource can
//     honor exactly one role and be typed as such, so the compiler rejects a call
//     to a write method it never implemented (no more runtime panic-to-reject).
//     Go cannot compile-time assert non-satisfaction, so the negative half is a
//     runtime interface assertion.
//
// What it tests:
//   - readOnlyProbe satisfies interfaces.Lister; the same value does NOT satisfy
//     interfaces.Store (it lacks Get/Create/Update/Delete/Exists).
func TestReadOnlyProbeIsListerNotStore(t *testing.T) {
	t.Parallel()

	var probe any = &readOnlyProbe{}

	_, isLister := probe.(interfaces.Lister[roleProbeEntity, roleProbeParams])
	assert.True(t, isLister, "a List-only type must satisfy Lister")

	_, isStore := probe.(interfaces.Store[roleProbeEntity, roleProbeParams, string])
	assert.False(t, isStore, "a Lister-only type must NOT satisfy the full Store")
}

// TestRoleInterfacesExist tests that the composable role interfaces
// (Reader/Lister/Writer/Deleter/Exister) exist in core/interfaces and are
// satisfied by the base structs that already implement the full CRUD surface.
//
// Why this test is important:
//   - splits the fat Store/Service contract into small roles so a
//     resource can embed only the operations it honors. If a role's signature
//     drifted from the method the bases already implement, the recomposition of
//     Store/Repository/Service would silently change the method set and break
//     every satisfier — the package-level var _ assertions above are the guard.
//
// What it tests:
//   - Each role interface is satisfied by BaseRepository (all five) and
//     BaseService (all but Exister), verified at compile time by the assertions.
func TestRoleInterfacesExist(t *testing.T) {
	t.Parallel()
	// The compile-time var _ assertions above are the assertion; reaching this
	// line means every role exists with the base-compatible signature.
}

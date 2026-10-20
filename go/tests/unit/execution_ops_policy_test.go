package unit_test

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// opDesc returns a valid descriptor named name with the given class and lifecycle.
func opDesc(
	name string,
	class types.SafetyClass,
	lc types.OpLifecycle,
) types.OperationDescriptor {
	return types.OperationDescriptor{
		Name: name, Group: name, Summary: name, SafetyClass: class,
		Lifecycle: lc, Timeout: time.Minute,
	}
}

// mockBuilders returns one Builder per descriptor whose operation is a MockOperation
// reporting that descriptor.
func mockBuilders(
	ctrl *gomock.Controller,
	descs []types.OperationDescriptor,
) map[string]ops.Builder[opsEnv] {
	b := make(map[string]ops.Builder[opsEnv], len(descs))
	for _, d := range descs {
		b[d.Name] = func(
			desc types.OperationDescriptor,
			_ opsEnv,
		) interfaces.Operation {
			op := mocks.NewMockOperation(ctrl)
			op.EXPECT().Descriptor().Return(desc).AnyTimes()
			return op
		}
	}
	return b
}

// mixedDescs is one operation per safety class and lifecycle the policy tests filter on.
func mixedDescs() []types.OperationDescriptor {
	return []types.OperationDescriptor{
		opDesc("ro", types.SafetyReadOnly, types.OpLifecycleBounded),
		opDesc("mut", types.SafetyMutating, types.OpLifecycleBounded),
		opDesc("destroy", types.SafetyDestructive, types.OpLifecycleBounded),
		opDesc("cred", types.SafetyCredentialed, types.OpLifecycleBounded),
		opDesc("long", types.SafetyReadOnly, types.OpLifecycleLongRunning),
		opDesc("ask", types.SafetyReadOnly, types.OpLifecycleInteractive),
	}
}

// listedNames returns the sorted names of the operations a registry lists.
func listedNames(reg *ops.Registry) []string {
	names := make([]string, 0, len(reg.List()))
	for _, op := range reg.List() {
		names = append(names, op.Descriptor().Name)
	}
	sort.Strings(names)
	return names
}

// newPolicyRegistry builds a registry over mixedDescs with mock builders under policy.
func newPolicyRegistry(
	t *testing.T,
	policy types.ExposurePolicy,
) (*ops.Registry, error) {
	t.Helper()
	descs := mixedDescs()
	return ops.NewFromConfig(ops.Config[opsEnv]{
		Policy:      policy,
		Descriptors: descs,
		Builders:    mockBuilders(gomock.NewController(t), descs),
	})
}

// allLifecycles admits every lifecycle, so a test isolates the safety-class filter.
func allLifecycles() []types.OpLifecycle {
	return []types.OpLifecycle{
		types.OpLifecycleBounded,
		types.OpLifecycleLongRunning,
		types.OpLifecycleInteractive,
	}
}

// TestExecutionOpsPolicy_ExcludesOperationAboveMaxSafety tests that a policy lists only
// the operations whose safety class ranks at or below its ceiling.
//
// Why this test is important:
//   - The MCP front-end's ceiling is mutating; an agent must never see a destructive or
//     credentialed operation.
//
// What it tests:
//   - max_safety mutating lists ro, mut, long and ask and omits destroy and cred; Lookup
//     of an excluded name reports false
func TestExecutionOpsPolicy_ExcludesOperationAboveMaxSafety(t *testing.T) {
	t.Parallel()

	reg, err := newPolicyRegistry(t, types.ExposurePolicy{
		MaxSafety: types.SafetyMutating, Lifecycles: allLifecycles(),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"ask", "long", "mut", "ro"}, listedNames(reg))
	_, ok := reg.Lookup("cred")
	assert.False(t, ok)
	_, ok = reg.Lookup("mut")
	assert.True(t, ok)
}

// TestExecutionOpsPolicy_ExcludesDisallowedLifecycle tests that a policy lists only
// operations whose lifecycle it names, and honours its allow and deny lists.
//
// Why this test is important:
//   - An interactive operation would block an MCP client waiting on a prompt; a
//     single-operation policy must admit exactly its operation.
//
// What it tests:
//   - lifecycles [bounded, long_running] omits ask; allow [ro, long] narrows to those
//     two; deny [ro] removes ro
func TestExecutionOpsPolicy_ExcludesDisallowedLifecycle(t *testing.T) {
	t.Parallel()

	noInteractive := []types.OpLifecycle{
		types.OpLifecycleBounded,
		types.OpLifecycleLongRunning,
	}
	reg, err := newPolicyRegistry(t, types.ExposurePolicy{
		MaxSafety: types.SafetyCredentialed, Lifecycles: noInteractive,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"cred", "destroy", "long", "mut", "ro"}, listedNames(reg))

	reg, err = newPolicyRegistry(t, types.ExposurePolicy{
		MaxSafety: types.SafetyCredentialed, Lifecycles: noInteractive,
		Allow: []string{"ro", "long"}, Deny: []string{"ro"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"long"}, listedNames(reg))
}

// TestExecutionOpsFactory_FailsOnUnknownOperationInPolicy tests that a policy naming an
// operation no descriptor declares is a construction error.
//
// Why this test is important:
//   - A typo in an allow list would otherwise silently expose nothing (or, in a deny
//     list, fail to hide what it meant to).
//
// What it tests:
//   - allow [nope] and deny [nope] each fail with CodeInvalidInput naming "nope"
func TestExecutionOpsFactory_FailsOnUnknownOperationInPolicy(t *testing.T) {
	t.Parallel()

	for _, p := range []types.ExposurePolicy{
		{
			MaxSafety:  types.SafetyReadOnly,
			Lifecycles: allLifecycles(),
			Allow:      []string{"nope"},
		},
		{
			MaxSafety:  types.SafetyReadOnly,
			Lifecycles: allLifecycles(),
			Deny:       []string{"nope"},
		},
	} {
		_, err := newPolicyRegistry(t, p)
		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
		assert.Contains(t, err.Error(), `"nope"`)
	}
}

// TestExecutionOpsFactory_FailsOnDescriptorWithoutImplementation tests that descriptors
// and builders must pair one-to-one.
//
// Why this test is important:
//   - A descriptor with no implementation advertises an operation no front-end can run;
//     an implementation with no descriptor has no safety class to filter on.
//
// What it tests:
//   - a descriptor with no builder, and a builder with no descriptor, each fail with
//     CodeInvalidInput naming the operation
func TestExecutionOpsFactory_FailsOnDescriptorWithoutImplementation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	descs := mixedDescs()
	builders := mockBuilders(ctrl, descs)
	delete(builders, "ro")
	_, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety:  types.SafetyCredentialed,
			Lifecycles: allLifecycles(),
		},
		Descriptors: descs,
		Builders:    builders,
	})
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), `"ro"`)

	builders = mockBuilders(ctrl, append(descs, opDesc("extra", types.SafetyReadOnly,
		types.OpLifecycleBounded)))
	_, err = ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety:  types.SafetyCredentialed,
			Lifecycles: allLifecycles(),
		},
		Descriptors: descs,
		Builders:    builders,
	})
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), `"extra"`)
}

// opsEnv is the environment the registry tests' builders capture.
type opsEnv struct {
	// Tag distinguishes environments.
	Tag string
}

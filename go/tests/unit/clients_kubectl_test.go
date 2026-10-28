package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/kubectl"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestClientsKubectl_Exec tests the kubectl binary backend's command lines.
//
// Why this test is important:
//   - A command that omits the pinned context or namespace acts on whatever cluster
//     the developer last used; that is how a dev command lands in staging.
//
// What it tests:
//   - Every call carries --context when configured; get, apply, rollout status and
//     current-context build their arguments with the namespace; auth can-i reads exit 0
//     as allowed and exit 1 with "no" as denied, and any other failure (an unreachable
//     cluster) is an error, never a denial; an apply with no files
//     is INVALID_INPUT without running kubectl; a failure is UPSTREAM;
//     the default kind is the stub, which also refuses an apply with no files; an
//     unknown kind is INVALID_INPUT.
func TestClientsKubectl_Exec(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	r := mocks.NewMockCommandRunner(gomock.NewController(t))
	c, err := kubectl.NewFromConfig(
		ctx,
		kubectl.Config{Kind: kubectl.KindExec, Context: "kind-dev"},
		kubectl.Deps{Decorations: fixtures.Decorations(), Runner: r},
	)
	require.NoError(t, err)

	r.EXPECT().RunBuffered(gomock.Any(), "", "kubectl", "--context", "kind-dev",
		"-n", "app", "get", "deploy", "api", "-o", "json").
		Return(interfaces.CmdResult{Stdout: []byte(`{"kind":"Deployment"}`)})
	out, err := c.Get(ctx, "app", "deploy", "api")
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"Deployment"}`, string(out))

	r.EXPECT().RunBuffered(gomock.Any(), "", "kubectl", "--context", "kind-dev",
		"apply", "-f", "a.yaml", "-f", "b.yaml").Return(interfaces.CmdResult{})
	require.NoError(t, c.Apply(ctx, "", "a.yaml", "b.yaml"))
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(c.Apply(ctx, "app")),
		"no files runs nothing")

	r.EXPECT().RunBuffered(gomock.Any(), "", "kubectl", "--context", "kind-dev",
		"-n", "app", "rollout", "status", "deploy/api", "--timeout=2m0s").
		Return(exitWith(1, "timed out waiting for the condition"))
	err = c.Rollout(ctx, "app", "deploy", "api", 2*time.Minute)
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))

	r.EXPECT().RunBuffered(gomock.Any(), "", "kubectl", "--context", "kind-dev",
		"config", "current-context").
		Return(interfaces.CmdResult{Stdout: []byte("kind-dev\n")})
	cur, err := c.CurrentContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, "kind-dev", cur)

	r.EXPECT().RunBuffered(gomock.Any(), "", "kubectl", "--context", "kind-dev",
		"auth", "can-i", "get", "pods").
		Return(interfaces.CmdResult{Stdout: []byte("yes\n")})
	allowed, err := c.AuthCanI(ctx, "get", "pods")
	require.NoError(t, err)
	assert.True(t, allowed)

	denied := exitWith(1, "")
	denied.Stdout = []byte("no\n")
	r.EXPECT().RunBuffered(gomock.Any(), "", "kubectl", "--context", "kind-dev",
		"auth", "can-i", "delete", "nodes").Return(denied)
	allowed, err = c.AuthCanI(ctx, "delete", "nodes")
	require.NoError(t, err)
	assert.False(t, allowed)

	r.EXPECT().RunBuffered(gomock.Any(), "", "kubectl", "--context", "kind-dev",
		"auth", "can-i", "get", "pods").
		Return(exitWith(1, "Unable to connect to the server: dial tcp: i/o timeout"))
	_, err = c.AuthCanI(ctx, "get", "pods")
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err), "unreachable is not a denial")

	stub, err := kubectl.NewFromConfig(ctx, kubectl.Config{},
		kubectl.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)
	cur, err = stub.CurrentContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, "stub", cur)
	allowed, err = stub.AuthCanI(ctx, "get", "pods")
	require.NoError(t, err)
	assert.False(t, allowed, "the stub has no cluster to grant anything")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(stub.Apply(ctx, "app")),
		"the stub keeps the contract")
	_, err = kubectl.NewFromConfig(ctx, kubectl.Config{Kind: "oc"},
		kubectl.Deps{Decorations: fixtures.Decorations()})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

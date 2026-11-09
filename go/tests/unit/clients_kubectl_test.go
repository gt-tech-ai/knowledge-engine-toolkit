package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/kubectl"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	r.EXPECT().RunBuffered(gomock.Any(), "", textKubectl, "--context", "kind-dev",
		"-n", textApp, textGet, textDeploy, textAPI, "-o", textStructuredFormat).
		Return(interfaces.CmdResult{Stdout: []byte(`{"kind":"Deployment"}`)})

	out, err := c.Get(ctx, textApp, textDeploy, textAPI)
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"Deployment"}`, string(out))

	r.EXPECT().RunBuffered(gomock.Any(), "", textKubectl, "--context", "kind-dev",
		"apply", "-f", textAYaml, "-f", "b.yaml").Return(interfaces.CmdResult{})
	require.NoError(t, c.Apply(ctx, "", textAYaml, "b.yaml"))
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(c.Apply(ctx, textApp)),
		"no files runs nothing")

	r.EXPECT().RunBuffered(gomock.Any(), "", textKubectl, "--context", "kind-dev",
		"-n", textApp, "rollout", textStatus, "deploy/api", "--timeout=2m0s").
		Return(exitWith(1, "timed out waiting for the condition"))

	err = c.Rollout(ctx, textApp, textDeploy, textAPI, 2*time.Minute)
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err))

	r.EXPECT().RunBuffered(gomock.Any(), "", textKubectl, "--context", "kind-dev",
		textConfig, "current-context").
		Return(interfaces.CmdResult{Stdout: []byte("kind-dev\n")})

	cur, err := c.CurrentContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, "kind-dev", cur)

	testClientsKubectlExecRest(ctx, t, r, c)
}

// testClientsKubectlExecRest continues TestClientsKubectl_Exec with its remaining steps.
func testClientsKubectlExecRest(
	ctx context.Context,
	t *testing.T,
	r *mocks.MockCommandRunner,
	c interfaces.KubectlClient,
) {
	t.Helper()

	var cur string

	var err error

	r.EXPECT().RunBuffered(gomock.Any(), "", textKubectl, "--context", "kind-dev",
		"auth", "can-i", textGet, textPods).
		Return(interfaces.CmdResult{Stdout: []byte("yes\n")})

	allowed, err := c.AuthCanI(ctx, textGet, textPods)
	require.NoError(t, err)
	assert.True(t, allowed)

	denied := exitWith(1, "")

	denied.Stdout = []byte("no\n")
	r.EXPECT().RunBuffered(gomock.Any(), "", textKubectl, "--context", "kind-dev",
		"auth", "can-i", textDelete, "nodes").Return(denied)

	allowed, err = c.AuthCanI(ctx, textDelete, "nodes")
	require.NoError(t, err)
	assert.False(t, allowed)

	r.EXPECT().RunBuffered(gomock.Any(), "", textKubectl, "--context", "kind-dev",
		"auth", "can-i", textGet, textPods).
		Return(exitWith(1, "Unable to connect to the server: dial tcp: i/o timeout"))

	_, err = c.AuthCanI(ctx, textGet, textPods)
	assert.Equal(t, apperr.CodeUpstream, apperr.Code(err), "unreachable is not a denial")

	stub, err := kubectl.NewFromConfig(ctx, kubectl.Config{},
		kubectl.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)

	cur, err = stub.CurrentContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, textStub, cur)

	allowed, err = stub.AuthCanI(ctx, textGet, textPods)
	require.NoError(t, err)
	assert.False(t, allowed, "the stub has no cluster to grant anything")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(stub.Apply(ctx, textApp)),
		"the stub keeps the contract")

	_, err = kubectl.NewFromConfig(ctx, kubectl.Config{Kind: "oc"},
		kubectl.Deps{Decorations: fixtures.Decorations()})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

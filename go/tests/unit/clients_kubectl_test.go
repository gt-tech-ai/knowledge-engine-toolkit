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
//     current-context build their arguments with the namespace; a failure is UPSTREAM;
//     the default kind is the stub; an unknown kind is INVALID_INPUT.
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

	stub, err := kubectl.NewFromConfig(ctx, kubectl.Config{},
		kubectl.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)
	cur, err = stub.CurrentContext(ctx)
	require.NoError(t, err)
	assert.Equal(t, "stub", cur)
	_, err = kubectl.NewFromConfig(ctx, kubectl.Config{Kind: "oc"},
		kubectl.Deps{Decorations: fixtures.Decorations()})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

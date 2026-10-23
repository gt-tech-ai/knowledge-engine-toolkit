package unit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	opscobra "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/cobra"
)

// treeSpec is a small CLI: a persistent --env, a dev group with a data-defined up leaf,
// and a typed check leaf.
func treeSpec() types.CommandSpec {
	upFlags := []types.FlagSpec{
		{Name: "minimal", Usage: "start the core services only"},
		{Name: "gpu", Default: true, Usage: "use the host GPU"},
		{Name: "only", Type: types.FlagStrings, Usage: "services to start"},
		{Name: "wait", Type: types.FlagDuration, Default: "2s", Usage: "readiness wait"},
	}
	return types.CommandSpec{
		Use: "app", Short: "the app CLI",
		Flags: []types.FlagSpec{
			{Name: "env", Type: types.FlagString, Persistent: true, Usage: "environment"},
		},
		Commands: []types.CommandSpec{
			{
				Use:   "dev",
				Short: "local stack",
				Group: "Develop",
				Commands: []types.CommandSpec{
					{
						Use:     "up [profile]",
						Short:   "start the stack",
						Op:      "dev.up",
						Aliases: []string{"start"},
						Flags:   upFlags,
						Args:    types.ArgSpec{Names: []string{"profile"}},
					},
				},
			},
			{Use: "check", Short: "run checks", Group: "Verify", Op: "check"},
			{Use: "secret", Hidden: true, Op: "check"},
		},
	}
}

// treeGot receives the tree's operation inputs.
type treeGot struct {
	// up is dev.up's input.
	up map[string]any

	// typed is check's input.
	typed cobraInput
}

// treeRegistry registers dev.up as a map input over the up flags and check over
// cobraInput; got receives their inputs.
func treeRegistry(t *testing.T, got *treeGot) *ops.Registry {
	t.Helper()
	up := treeSpec().Commands[0].Commands[0]
	flags := append([]types.FlagSpec{{Name: "env", Type: types.FlagString}}, up.Flags...)
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety: types.SafetyCredentialed, Lifecycles: allLifecycles(),
		},
		Descriptors: []types.OperationDescriptor{
			opDesc("dev.up", types.SafetyMutating, types.OpLifecycleBounded),
			opDesc("check", types.SafetyReadOnly, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			"dev.up": ops.MapInput(flags, up.Args, func(
				_ context.Context, in map[string]any, _ opsEnv, _ interfaces.ExecutionObserver,
			) error {
				got.up = in
				return nil
			}),
			"check": ops.Func(func(
				_ context.Context, in *cobraInput, _ opsEnv, _ interfaces.ExecutionObserver,
			) error {
				got.typed = *in
				return nil
			}),
		},
	})
	require.NoError(t, err)
	return reg
}

// TestTransportCobra_Tree tests a command tree built from data.
//
// Why this test is important:
//   - a CLI whose commands are data has no per-command Go wiring; its flags, defaults,
//     inherited persistent flags, arguments, aliases and help groups must all reach the
//     operation and the help exactly as declared.
//
// What it tests:
//   - a typed leaf binds its input's struct tags;
//   - a leaf's input carries its set flags, defaults, the ancestor's persistent flag and
//     its positional argument, through an alias; a group prints help listing children
//     under their groups and hides hidden commands; the map input's schema lists the
//     flags; an unknown operation fails the build.
func TestTransportCobra_Tree(t *testing.T) {
	t.Parallel()
	var got treeGot
	reg := treeRegistry(t, &got)
	root, err := opscobra.Tree(treeSpec(), reg, opscobra.Options{})
	require.NoError(t, err)
	root.SetArgs(
		[]string{
			"dev",
			"start",
			"--env",
			"staging",
			"--only",
			"api,web",
			"--minimal",
			"full",
		},
	)
	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.Equal(t, map[string]any{
		"env": "staging", "only": []any{"api", "web"}, "minimal": true, "gpu": true,
		"wait": float64(2e9), "profile": "full",
	}, roundTrip(t, got.up))

	root, err = opscobra.Tree(treeSpec(), reg, opscobra.Options{})
	require.NoError(t, err)
	root.SetArgs([]string{"check", "--env", "dev", "--workers", "3", "svc"})
	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.Equal(t, cobraInput{Env: "dev", Workers: 3, Target: "svc"}, got.typed)

	root, err = opscobra.Tree(treeSpec(), reg, opscobra.Options{})
	require.NoError(t, err)
	var help bytes.Buffer
	root.SetOut(&help)
	root.SetArgs([]string{})
	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.Contains(t, help.String(), "Develop:\n  dev")
	assert.Contains(t, help.String(), "Verify:\n  check")
	assert.NotContains(t, help.String(), "secret")

	op, err := reg.Find("dev.up")
	require.NoError(t, err)
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(op.InputSchema(), &schema))
	assert.Contains(t, schema.Properties, "only")
	assert.Contains(t, schema.Properties, "profile")

	bad := treeSpec()
	bad.Commands[1].Op = "nope"
	_, err = opscobra.Tree(bad, reg, opscobra.Options{})
	require.Error(t, err)
}

// roundTrip normalizes v through JSON, as the operation's decoder sees it.
func roundTrip(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

package unit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
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

// TestTransportCobra_TreePassesASecretFlag tests that the CLI still takes a map
// operation's secret flag.
//
// Why this test is important:
//   - A secret flag is refused over MCP and CronJob arguments (ops.Decode); the terminal
//     is the one front-end where a human types it, so dropping it there would leave the
//     operation without its credential.
//
// What it tests:
//   - `kb --client-secret s --client-id a` runs the operation with both keys set.
func TestTransportCobra_TreePassesASecretFlag(t *testing.T) {
	t.Parallel()
	flags := []types.FlagSpec{
		{Name: "client-id", Type: types.FlagString},
		{Name: "client-secret", Type: types.FlagString, Secret: true},
	}
	var got map[string]any
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety: types.SafetyCredentialed, Lifecycles: allLifecycles(),
		},
		Descriptors: []types.OperationDescriptor{
			opDesc("kb", types.SafetyCredentialed, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			"kb": ops.MapInput(flags, types.ArgSpec{},
				func(
					_ context.Context, in map[string]any, _ opsEnv, _ interfaces.ExecutionObserver,
				) error {
					got = in
					return nil
				}),
		},
	})
	require.NoError(t, err)
	root, err := opscobra.Tree(types.CommandSpec{
		Use: "app", Commands: []types.CommandSpec{{Use: "kb", Op: "kb", Flags: flags}},
	}, reg, opscobra.Options{})
	require.NoError(t, err)
	root.SetArgs([]string{"kb", "--client-secret", "s", "--client-id", "a"})

	require.NoError(t, root.ExecuteContext(t.Context()))

	assert.Equal(t, "s", got["client_secret"])
	assert.Equal(t, "a", got["client_id"])
}

// TestTransportCobra_TreeResolvesPerRun tests a tree whose registry and observer are
// resolved when a command runs.
//
// Why this test is important:
//   - a CLI that finds its workspace, policy and console in a PersistentPreRun builds its
//     registry after the tree is parsed; the tree must build without one and take the
//     registry and observer that run resolves, or every leaf would need per-command Go.
//
// What it tests:
//   - Tree builds with a nil registry; `dev up` resolves once for "dev.up" and the
//     operation gets that run's observer and its input; each leaf carries the operation
//     annotation and a group none; a leaf naming an unknown operation fails when it runs.
func TestTransportCobra_TreeResolvesPerRun(t *testing.T) {
	t.Parallel()
	var got treeGot
	obs := mocks.NewMockExecutionObserver(gomock.NewController(t))
	flags := []types.FlagSpec{{Name: "env", Type: types.FlagString}}
	var seen interfaces.ExecutionObserver
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety: types.SafetyCredentialed, Lifecycles: allLifecycles(),
		},
		Descriptors: []types.OperationDescriptor{
			opDesc("dev.up", types.SafetyMutating, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			"dev.up": ops.MapInput(flags, types.ArgSpec{}, func(
				_ context.Context, in map[string]any, _ opsEnv, o interfaces.ExecutionObserver,
			) error {
				got.up, seen = in, o
				return nil
			}),
		},
	})
	require.NoError(t, err)
	var resolved []string
	opts := opscobra.Options{Resolve: func(
		_ context.Context, op string,
	) (*ops.Registry, interfaces.ExecutionObserver, error) {
		resolved = append(resolved, op)
		return reg, obs, nil
	}}
	spec := types.CommandSpec{Use: "app", Commands: []types.CommandSpec{
		{
			Use:      "dev",
			Commands: []types.CommandSpec{{Use: "up", Op: "dev.up", Flags: flags}},
		},
		{Use: "gone", Op: "nope"},
	}}

	root, err := opscobra.Tree(spec, nil, opts)
	require.NoError(t, err)
	root.SetArgs([]string{"dev", "up", "--env", "staging"})
	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.Equal(t, []string{"dev.up"}, resolved)
	assert.Equal(t, map[string]any{"env": "staging"}, got.up)
	assert.Same(t, obs, seen)

	dev, _, err := root.Find([]string{"dev"})
	require.NoError(t, err)
	up, _, err := root.Find([]string{"dev", "up"})
	require.NoError(t, err)
	assert.Empty(t, dev.Annotations[opscobra.OperationAnnotation])
	assert.Equal(t, "dev.up", up.Annotations[opscobra.OperationAnnotation])

	root, err = opscobra.Tree(spec, nil, opts)
	require.NoError(t, err)
	root.SetArgs([]string{"gone"})
	require.Error(t, root.ExecuteContext(t.Context()))
}

// TestTransportCobra_TreeStringArrayFlag tests a repeatable flag that never splits.
//
// Why this test is important:
//   - a value such as a `name:type:help` spec or a unit path may hold a comma; a
//     comma-splitting list flag would turn one value into two.
//
// What it tests:
//   - `--tag a,b --tag c` reaches the operation as ["a,b", "c"], and the input schema
//     types the flag as a string array.
func TestTransportCobra_TreeStringArrayFlag(t *testing.T) {
	t.Parallel()
	flags := []types.FlagSpec{{Name: "tag", Type: types.FlagStringArray}}
	var got map[string]any
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety: types.SafetyCredentialed, Lifecycles: allLifecycles(),
		},
		Descriptors: []types.OperationDescriptor{
			opDesc("tag", types.SafetyReadOnly, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			"tag": ops.MapInput(flags, types.ArgSpec{}, func(
				_ context.Context, in map[string]any, _ opsEnv, _ interfaces.ExecutionObserver,
			) error {
				got = in
				return nil
			}),
		},
	})
	require.NoError(t, err)
	root, err := opscobra.Tree(types.CommandSpec{
		Use: "app", Commands: []types.CommandSpec{{Use: "tag", Op: "tag", Flags: flags}},
	}, reg, opscobra.Options{})
	require.NoError(t, err)
	root.SetArgs([]string{"tag", "--tag", "a,b", "--tag", "c"})
	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.Equal(t, []string{"a,b", "c"}, got["tag"])

	op, err := reg.Find("tag")
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"type":"object","additionalProperties":false,`+
			`"properties":{"tag":{"type":"array","items":{"type":"string"}}}}`,
		string(op.InputSchema()))
}

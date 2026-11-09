package unit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	spf "github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	opscobra "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/cobra"
)

// treeSpec is a small CLI: a persistent --env, a dev group with a data-defined up leaf,
// and a typed check leaf.
func treeSpec() types.CommandSpec {
	upFlags := []types.FlagSpec{
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = "minimal"
			row.Usage = "start the core services only"
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = "gpu"
			row.Default = true
			row.Usage = "use the host GPU"
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textOnly
			row.Type = types.FlagStrings
			row.Usage = "services to start"
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textWait
			row.Type = types.FlagDuration
			row.Default = "2s"
			row.Usage = "readiness wait"
		}),
	}

	return fixtures.Build(func(row *types.CommandSpec) {
		row.Use = textApp
		row.Short = "the app CLI"
		row.Flags = []types.FlagSpec{
			fixtures.Build(func(row *types.FlagSpec) {
				row.Name = textEnv
				row.Type = types.FlagString
				row.Persistent = true
				row.Usage = "environment"
			}),
		}
		row.Commands = treeSpecTable(upFlags)
	})
}

// treeSpecTable builds the table treeSpec runs.
func treeSpecTable(upFlags []types.FlagSpec) []types.CommandSpec {
	return []types.CommandSpec{
		fixtures.Build(func(row *types.CommandSpec) {
			row.Use = textDev
			row.Short = "local stack"
			row.Group = "Develop"
			row.Commands = []types.CommandSpec{
				fixtures.Build(func(row *types.CommandSpec) {
					row.Use = "up [profile]"
					row.Short = "start the stack"
					row.Op = textDevUp
					row.Aliases = []string{"start"}
					row.Flags = upFlags
					row.Args = types.ArgSpec{
						Names: []string{textProfile},
						Rest:  "",
						Min:   0,
					}
				}),
			}
		}),
		fixtures.Build(func(row *types.CommandSpec) {
			row.Use = textCheck
			row.Short = "run checks"
			row.Group = "Verify"
			row.Op = textCheck
		}),
		fixtures.Build(func(row *types.CommandSpec) {
			row.Use = textSecret
			row.Hidden = true
			row.Op = textCheck
		}),
	}
}

// treeGot receives the tree's operation inputs.
type treeGot struct {
	// observer is the execution observer the run resolved.
	observer interfaces.ExecutionObserver

	// up is dev.up's input.
	up map[string]any

	// typed is check's input.
	typed cobraInput
}

// treeRegistry registers dev.up as a map input over the up flags and check over
// cobraInput; got receives their inputs.
func treeRegistry(t *testing.T, got *treeGot) *ops.Registry {
	t.Helper()

	commandSpec := treeSpec().Commands[0].Commands[0]
	flags := append(
		[]types.FlagSpec{
			fixtures.Build(func(row *types.FlagSpec) {
				row.Name = textEnv
				row.Type = types.FlagString
			}),
		},
		commandSpec.Flags...)
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: fixtures.Build(func(row *types.ExposurePolicy) {
			row.MaxSafety = types.SafetyCredentialed
			row.Lifecycles = allLifecycles()
		}),
		Descriptors: []types.OperationDescriptor{
			opDesc(textDevUp, types.SafetyMutating, types.OpLifecycleBounded),
			opDesc(textCheck, types.SafetyReadOnly, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			textDevUp: ops.MapInput(flags, commandSpec.Args, func(
				_ context.Context, in map[string]any, _ opsEnv, _ interfaces.ExecutionObserver,
			) error {
				got.up = in

				return nil
			}),
			textCheck: ops.Func(func(
				_ context.Context, in *cobraInput, _ opsEnv, _ interfaces.ExecutionObserver,
			) error {
				got.typed = *in

				return nil
			}),
		},
		Env: stdx.Zero[opsEnv](),
		Log: nil,
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
	root, err := opscobra.Tree(new(treeSpec()), reg, stdx.Zero[opscobra.Options]())
	require.NoError(t, err)
	root.SetArgs(
		[]string{
			textDev,
			"start",
			textEnvFlag,
			textStaging,
			textOnlyFlag,
			"api,web",
			"--minimal",
			textFull,
		},
	)
	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.Equal(t, map[string]any{
		textEnv:     textStaging,
		textOnly:    []any{textAPI, textWeb},
		"minimal":   true,
		"gpu":       true,
		textWait:    float64(2e9),
		textProfile: textFull,
	}, roundTrip(t, got.up))

	root, err = opscobra.Tree(new(treeSpec()), reg, stdx.Zero[opscobra.Options]())
	require.NoError(t, err)
	root.SetArgs([]string{textCheck, textEnvFlag, textDev, "--workers", "3", textSvc})

	testTransportCobraTreeFlags(t, &got, reg, root)
}

// testTransportCobraTreeFlags continues TestTransportCobra_Tree: flags.
func testTransportCobraTreeFlags(
	t *testing.T,
	got *treeGot,
	reg *ops.Registry,
	root *spf.Command,
) {
	t.Helper()

	var err error

	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.Equal(
		t,
		fixtures.Build(func(row *cobraInput) {
			row.Env = textDev
			row.Workers = 3
			row.Target = textSvc
		}),
		got.typed,
	)

	root, err = opscobra.Tree(new(treeSpec()), reg, stdx.Zero[opscobra.Options]())
	require.NoError(t, err)

	var help bytes.Buffer

	root.SetOut(&help)
	root.SetArgs([]string{})
	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.Contains(t, help.String(), "Develop:\n  dev")
	assert.Contains(t, help.String(), "Verify:\n  check")
	assert.NotContains(t, help.String(), textSecret)

	operation, err := reg.Find(textDevUp)
	require.NoError(t, err)

	var schema struct {
		Properties map[string]any `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(operation.InputSchema(), &schema))
	assert.Contains(t, schema.Properties, textOnly)
	assert.Contains(t, schema.Properties, textProfile)

	bad := treeSpec()

	bad.Commands[1].Op = textNope
	_, err = opscobra.Tree(&bad, reg, stdx.Zero[opscobra.Options]())
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
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textClientID
			row.Type = types.FlagString
		}),
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textClientSecretKebab
			row.Type = types.FlagString
			row.Secret = true
		}),
	}

	var got map[string]any

	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: fixtures.Build(func(row *types.ExposurePolicy) {
			row.MaxSafety = types.SafetyCredentialed
			row.Lifecycles = allLifecycles()
		}),
		Descriptors: []types.OperationDescriptor{
			opDesc("kb", types.SafetyCredentialed, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			"kb": ops.MapInput(flags, stdx.Zero[types.ArgSpec](),
				func(
					_ context.Context, in map[string]any, _ opsEnv, _ interfaces.ExecutionObserver,
				) error {
					got = in

					return nil
				}),
		},
		Env: stdx.Zero[opsEnv](),
		Log: nil,
	})
	require.NoError(t, err)

	root, err := opscobra.Tree(fixtures.BuildRef(func(row *types.CommandSpec) {
		row.Use = textApp
		row.Commands = []types.CommandSpec{
			fixtures.Build(func(row *types.CommandSpec) {
				row.Use = "kb"
				row.Op = "kb"
				row.Flags = flags
			}),
		}
	}), reg, stdx.Zero[opscobra.Options]())
	require.NoError(t, err)
	root.SetArgs([]string{"kb", "--client-secret", textS, "--client-id", textA})

	require.NoError(t, root.ExecuteContext(t.Context()))

	assert.Equal(t, textS, got[textClientSecret])
	assert.Equal(t, textA, got["client_id"])
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
	flags := []types.FlagSpec{
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textEnv
			row.Type = types.FlagString
		}),
	}

	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: fixtures.Build(func(row *types.ExposurePolicy) {
			row.MaxSafety = types.SafetyCredentialed
			row.Lifecycles = allLifecycles()
		}),
		Descriptors: []types.OperationDescriptor{
			opDesc(textDevUp, types.SafetyMutating, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			textDevUp: ops.MapInput(flags, stdx.Zero[types.ArgSpec](), func(
				_ context.Context, in map[string]any, _ opsEnv, o interfaces.ExecutionObserver,
			) error {
				got.up, got.observer = in, o

				return nil
			}),
		},
		Env: stdx.Zero[opsEnv](),
		Log: nil,
	})
	require.NoError(t, err)

	var resolved []string

	testTransportCobraTreeResolvesPerRunResolvesSpec(
		t,
		&got,
		obs,
		flags,
		reg,
		resolved,
	)
}

// testTransportCobraTreeResolvesPerRunResolvesSpec continues
// TestTransportCobra_TreeResolvesPerRun.
func testTransportCobraTreeResolvesPerRunResolvesSpec(
	t *testing.T,
	got *treeGot,
	obs *mocks.MockExecutionObserver,
	flags []types.FlagSpec,
	reg *ops.Registry,
	resolved []string,
) {
	t.Helper()

	opts := fixtures.Build(func(row *opscobra.Options) {
		row.Resolve = func(
			_ context.Context, op string,
		) (*ops.Registry, interfaces.ExecutionObserver, error) {
			resolved = append(resolved, op)

			return reg, obs, nil
		}
	})
	spec := fixtures.Build(func(row *types.CommandSpec) {
		row.Use = textApp
		row.Commands = []types.CommandSpec{
			fixtures.Build(func(row *types.CommandSpec) {
				row.Use = textDev
				row.Commands = []types.CommandSpec{
					fixtures.Build(func(row *types.CommandSpec) {
						row.Use = textUp
						row.Op = textDevUp
						row.Flags = flags
					}),
				}
			}),
			fixtures.Build(func(row *types.CommandSpec) {
				row.Use = textGone
				row.Op = textNope
			}),
		}
	})

	root, err := opscobra.Tree(&spec, nil, opts)
	require.NoError(t, err)
	root.SetArgs([]string{textDev, textUp, textEnvFlag, textStaging})
	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.Equal(t, []string{textDevUp}, resolved)
	assert.Equal(t, map[string]any{textEnv: textStaging}, got.up)
	assert.Same(t, obs, got.observer)

	testTransportCobraTreeResolvesPerRunAnnotations(t, opts, &spec, root)
}

// testTransportCobraTreeResolvesPerRunAnnotations continues
// TestTransportCobra_TreeResolvesPerRun: annotations.
func testTransportCobraTreeResolvesPerRunAnnotations(
	t *testing.T,
	opts opscobra.Options,
	spec *types.CommandSpec,
	root *spf.Command,
) {
	t.Helper()

	var err error

	dev, _, err := root.Find([]string{textDev})
	require.NoError(t, err)

	up, _, err := root.Find([]string{textDev, textUp})
	require.NoError(t, err)
	assert.Empty(t, dev.Annotations[opscobra.OperationAnnotation])
	assert.Equal(t, textDevUp, up.Annotations[opscobra.OperationAnnotation])

	root, err = opscobra.Tree(spec, nil, opts)
	require.NoError(t, err)
	root.SetArgs([]string{textGone})
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

	flags := []types.FlagSpec{
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textTag
			row.Type = types.FlagStringArray
		}),
	}

	var got map[string]any

	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: fixtures.Build(func(row *types.ExposurePolicy) {
			row.MaxSafety = types.SafetyCredentialed
			row.Lifecycles = allLifecycles()
		}),
		Descriptors: []types.OperationDescriptor{
			opDesc(textTag, types.SafetyReadOnly, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			textTag: ops.MapInput(flags, stdx.Zero[types.ArgSpec](), func(
				_ context.Context, in map[string]any, _ opsEnv, _ interfaces.ExecutionObserver,
			) error {
				got = in

				return nil
			}),
		},
		Env: stdx.Zero[opsEnv](),
		Log: nil,
	})
	require.NoError(t, err)

	root, err := opscobra.Tree(fixtures.BuildRef(func(row *types.CommandSpec) {
		row.Use = textApp
		row.Commands = []types.CommandSpec{
			fixtures.Build(func(row *types.CommandSpec) {
				row.Use = textTag
				row.Op = textTag
				row.Flags = flags
			}),
		}
	}), reg, stdx.Zero[opscobra.Options]())
	require.NoError(t, err)
	root.SetArgs([]string{textTag, "--tag", "a,b", "--tag", textC})
	require.NoError(t, root.ExecuteContext(t.Context()))
	assert.Equal(t, []string{"a,b", textC}, got[textTag])

	op, err := reg.Find(textTag)
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"type":"object","additionalProperties":false,`+
			`"properties":{"tag":{"type":"array","items":{"type":"string"}}}}`,
		string(op.InputSchema()))
}

// TestTransportCobra_TreeRunHooks tests the hooks around a leaf's run.
//
// Why this test is important:
//   - some inputs come from outside the flags (stdin, the console's verbosity), one flag
//     can select a sibling operation (a --fix that runs the mutating twin), and a CLI
//     opens and closes a log group around each run whether it fails or not; without the
//     hooks each of those needs its own hand-built command.
//
// What it tests:
//   - Before sees the leaf's input, adds a key and selects another operation, which runs
//     with that input; After gets the selected operation and the run's error and its
//     return is the command's error.
func TestTransportCobra_TreeRunHooks(t *testing.T) {
	t.Parallel()

	flags := []types.FlagSpec{
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textFix
		}),
	}
	ran := map[string]map[string]any{}

	testTransportCobraTreeRunHooksBuildsOps(t, flags, ran)
}

// testTransportCobraTreeRunHooksBuildsOps continues testTransportCobraTreeRunHooks:
// builds ops.
func testTransportCobraTreeRunHooksBuildsOps(
	t *testing.T,
	flags []types.FlagSpec,
	ran map[string]map[string]any,
) {
	t.Helper()

	run := func(name string) ops.Builder[opsEnv] {
		return ops.MapInput(
			slices.Concat(
				flags,
				[]types.FlagSpec{
					fixtures.Build(func(row *types.FlagSpec) {
						row.Name = textExtra
						row.Type = types.FlagString
					}),
				},
			),
			stdx.Zero[types.ArgSpec](),
			func(
				_ context.Context, in map[string]any, _ opsEnv, _ interfaces.ExecutionObserver,
			) error {
				ran[name] = in

				return apperr.New(apperr.CodeInternal, textBoom)
			},
		)
	}
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: fixtures.Build(func(row *types.ExposurePolicy) {
			row.MaxSafety = types.SafetyCredentialed
			row.Lifecycles = allLifecycles()
		}),
		Descriptors: []types.OperationDescriptor{
			opDesc(textLint, types.SafetyReadOnly, types.OpLifecycleBounded),
			opDesc("lint.fix", types.SafetyMutating, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			textLint:   run(textLint),
			"lint.fix": run("lint.fix"),
		},
		Env: stdx.Zero[opsEnv](),
		Log: nil,
	})

	testTransportCobraTreeRunHooksAfterHooks(t, flags, ran, reg, err)
}

// testTransportCobraTreeRunHooksAfterHooks continues
// testTransportCobraTreeRunHooksBuildsOps: after hooks.
func testTransportCobraTreeRunHooksAfterHooks(
	t *testing.T,
	flags []types.FlagSpec,
	ran map[string]map[string]any,
	reg *ops.Registry,
	err error,
) {
	t.Helper()

	require.NoError(t, err)

	var after []string

	opts := fixtures.Build(func(row *opscobra.Options) {
		row.Before = func(_ *spf.Command, operation string, in map[string]any) (string, error) {
			in[textExtra] = textX
			if fix := stdx.As[bool](in[textFix]); fix {
				return operation + ".fix", nil
			}

			return operation, nil
		}
		row.After = func(_ *spf.Command, op string, err error) error {
			after = append(after, op)

			return apperr.Wrap(err, apperr.CodeInternal, textAfter)
		}
	})
	root, err := opscobra.Tree(fixtures.BuildRef(func(row *types.CommandSpec) {
		row.Use = textApp
		row.Commands = []types.CommandSpec{
			fixtures.Build(func(row *types.CommandSpec) {
				row.Use = textLint
				row.Op = textLint
				row.Flags = flags
			}),
		}
	}), reg, opts)
	require.NoError(t, err)
	root.SetArgs([]string{textLint, textFixFlag})

	err = root.ExecuteContext(t.Context())

	require.ErrorContains(t, err, textAfter)
	assert.NotContains(t, ran, textLint)
	assert.Equal(t, map[string]any{textFix: true, textExtra: textX}, ran["lint.fix"])
	assert.Equal(t, []string{"lint.fix"}, after)
}

// TestTransportCobra_TreeDurationDefault tests a duration flag's declared default.
//
// Why this test is important:
//   - the help prints a flag's default and a run reads it when the flag is not given; a
//     default applied as if typed would show 0s and count the flag as set.
//
// What it tests:
//   - a `default: 2m` duration flag reports 2m0s as its default and is not changed.
func TestTransportCobra_TreeDurationDefault(t *testing.T) {
	t.Parallel()

	root, err := opscobra.Tree(fixtures.BuildRef(func(row *types.CommandSpec) {
		row.Use = textApp
		row.Commands = []types.CommandSpec{fixtures.Build(func(row *types.CommandSpec) {
			row.Use = "grp"
			row.Flags = []types.FlagSpec{
				fixtures.Build(func(row *types.FlagSpec) {
					row.Name = textWait
					row.Type = types.FlagDuration
					row.Default = "2m"
				}),
			}
		})}
	}), nil, stdx.Zero[opscobra.Options]())
	require.NoError(t, err)

	grp, _, err := root.Find([]string{"grp"})
	require.NoError(t, err)

	f := grp.Flags().Lookup(textWait)
	require.NotNil(t, f)
	assert.Equal(t, text2m0s, f.DefValue)
	assert.False(t, f.Changed)
}

// TestTransportCobra_TreeRunOverride tests the hook that replaces a leaf's local run.
//
// Why this test is important:
//   - a scheduled job runs the same command tree with no human at the terminal, so its
//     leaf must run the way a cron entrypoint does (no secret flag, no interactive
//     operation) rather than the CLI's local run, which admits both; without the hook
//     the scheduled front-end needs a second command tree.
//
// What it tests:
//   - with Run set, the leaf hands it the run's registry, the operation and the input
//     as JSON, its error is the command's, and the operation's own run never starts;
//     passing RunLocal as Run runs the operation as the default does.
func TestTransportCobra_TreeRunOverride(t *testing.T) {
	t.Parallel()

	flags := []types.FlagSpec{
		fixtures.Build(func(row *types.FlagSpec) {
			row.Name = textEnv
			row.Type = types.FlagString
		}),
	}

	ran := new(capturedRun)

	testTransportCobraTreeRunOverrideRuns(t, flags, ran)
}

// testTransportCobraTreeRunOverrideRuns continues
// testTransportCobraTreeRunOverride: runs.
func testTransportCobraTreeRunOverrideRuns(
	t *testing.T,
	flags []types.FlagSpec,
	ran *capturedRun,
) {
	t.Helper()

	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: fixtures.Build(func(row *types.ExposurePolicy) {
			row.MaxSafety = types.SafetyCredentialed
			row.Lifecycles = allLifecycles()
		}),
		Descriptors: []types.OperationDescriptor{
			opDesc(textDrift, types.SafetyReadOnly, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			textDrift: ops.MapInput(flags, stdx.Zero[types.ArgSpec](), func(
				_ context.Context, in map[string]any, _ opsEnv, _ interfaces.ExecutionObserver,
			) error {
				ran.in = in

				return nil
			}),
		},
		Env: stdx.Zero[opsEnv](),
		Log: nil,
	})
	require.NoError(t, err)

	spec := fixtures.Build(func(row *types.CommandSpec) {
		row.Use = textApp
		row.Commands = []types.CommandSpec{
			fixtures.Build(func(row *types.CommandSpec) {
				row.Use = textDrift
				row.Op = textDrift
				row.Flags = flags
			}),
		}
	})

	testTransportCobraTreeRunOverrideSchedules(t, ran, reg, &spec)
}

// testTransportCobraTreeRunOverrideSchedules continues
// testTransportCobraTreeRunOverrideRuns: schedules.
func testTransportCobraTreeRunOverrideSchedules(
	t *testing.T,
	ran *capturedRun,
	reg *ops.Registry,
	specRef *types.CommandSpec,
) {
	t.Helper()

	spec := *specRef

	var err error

	tree := func(run func(
		context.Context, *ops.Registry, string, []byte, interfaces.ExecutionObserver,
	) error,
	) error {
		root, opErr := opscobra.Tree(
			&spec,
			reg,
			fixtures.Build(func(row *opscobra.Options) {
				row.Run = run
			}),
		)
		require.NoError(t, opErr)
		root.SetArgs([]string{textDrift, textEnvFlag, textStaging})

		return root.ExecuteContext(t.Context())
	}

	var got struct {
		reg *ops.Registry
		op  string
		raw string
	}

	err = tree(func(
		_ context.Context, r *ops.Registry, op string, raw []byte,
		_ interfaces.ExecutionObserver,
	) error {
		got.reg, got.op, got.raw = r, op, string(raw)

		return apperr.New(apperr.CodeForbidden, "scheduled")
	})

	require.ErrorContains(t, err, "scheduled")
	assert.Same(t, reg, got.reg)
	assert.Equal(t, textDrift, got.op)
	assert.JSONEq(t, `{"env":"staging"}`, got.raw)
	assert.Nil(t, ran.in, "the override replaces the operation's run")

	require.NoError(t, tree(opscobra.RunLocal))
	assert.Equal(t, map[string]any{textEnv: textStaging}, ran.in)
}

// capturedRun holds the input a run override received.
type capturedRun struct {
	// in is the operation input, nil until the override ran.
	in map[string]any
}

const (
	// textDevUp is the literal "dev.up".
	textDevUp = "dev.up"

	// textEnvFlag is the literal "--env".
	textEnvFlag = "--env"
)

package unit_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
	opscobra "github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport/cobra"
)

// cobraInput exercises every flag kind and positional arguments.
type cobraInput struct {
	// Env is required.
	Env string `json:"env" flag:"env,e,environment to check" jsonschema:"required"`

	// Target is the first positional argument.
	Target string `json:"target" arg:"0"`

	// Only is a string-slice flag.
	Only []string `json:"only" flag:"only,,limit to these"`

	// Rest are the remaining positional arguments.
	Rest []string `json:"rest" arg:"1"`

	// Wait is a duration flag.
	Wait time.Duration `json:"wait" flag:"wait,,how long to wait"`

	// Workers is an int flag.
	Workers int `json:"workers" flag:"workers,w,parallel workers"`

	// Fix is a bool flag.
	Fix bool `json:"fix" flag:"fix,,apply fixes"`
}

// cobraBadInput has a flag of an unsupported type.
type cobraBadInput struct {
	// Ratio is a float, which the front-end does not support.
	Ratio float64 `json:"ratio" flag:"ratio,,a ratio"`
}

// cobraRegistry registers "check" over cobraInput and "bad" over cobraBadInput; got
// receives check's input.
func cobraRegistry(t *testing.T, got *cobraInput) *ops.Registry {
	t.Helper()
	reg, err := ops.NewFromConfig(ops.Config[opsEnv]{
		Policy: types.ExposurePolicy{
			MaxSafety: types.SafetyCredentialed, Lifecycles: allLifecycles(),
		},
		Descriptors: []types.OperationDescriptor{
			opDesc("check", types.SafetyReadOnly, types.OpLifecycleBounded),
			opDesc("bad", types.SafetyReadOnly, types.OpLifecycleBounded),
		},
		Builders: map[string]ops.Builder[opsEnv]{
			"check": ops.Func(func(
				_ context.Context, in *cobraInput, _ opsEnv, _ interfaces.ExecutionObserver,
			) error {
				*got = *in
				return nil
			}),
			"bad": ops.Func(func(
				context.Context, *cobraBadInput, opsEnv, interfaces.ExecutionObserver,
			) error {
				return nil
			}),
		},
	})
	require.NoError(t, err)
	return reg
}

// execCobra runs args through the command for name.
func execCobra(t *testing.T, reg *ops.Registry, args ...string) error {
	t.Helper()
	cmd, err := opscobra.Command(reg, "check", opscobra.Options{})
	require.NoError(t, err)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return apperr.Wrap(cmd.ExecuteContext(t.Context()), apperr.CodeInternal, "execute")
}

// TestTransportCobra_Command tests the Cobra front-end.
//
// Why this test is important:
//   - A command's flags are derived from the operation's input, so a CLI and an agent
//     run the same operation the same way; a flag that binds the wrong field, or a
//     missing required flag that runs anyway, runs the wrong thing.
//
// What it tests:
//   - The command is named after the operation and described by its summary; every
//     flag kind parses into its field, with its shorthand; positional arguments fill
//     their fields, the last slice taking the rest; a missing required flag fails
//     without running; an unsupported field type fails construction with
//     INVALID_INPUT naming the field; an unknown operation is NOT_FOUND.
func TestTransportCobra_Command(t *testing.T) {
	t.Parallel()
	var got cobraInput
	reg := cobraRegistry(t, &got)
	cmd, err := opscobra.Command(reg, "check", opscobra.Options{})
	require.NoError(t, err)
	assert.Equal(t, "check", cmd.Name())
	assert.Equal(t, "check", cmd.Short)

	require.NoError(t, execCobra(t, reg, "-e", "staging", "--fix", "-w", "3",
		"--wait", "2s", "--only", "a,b", "api", "x", "y"))
	assert.Equal(t, cobraInput{
		Env: "staging", Fix: true, Workers: 3, Wait: 2 * time.Second,
		Only: []string{"a", "b"}, Target: "api", Rest: []string{"x", "y"},
	}, got)

	got = cobraInput{}
	require.Error(t, execCobra(t, reg, "--fix"))
	assert.Equal(t, cobraInput{}, got, "nothing ran")

	_, err = opscobra.Command(reg, "bad", opscobra.Options{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	assert.Contains(t, err.Error(), "Ratio")
	_, err = opscobra.Command(reg, "nope", opscobra.Options{})
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
}

// TestTransportCobra_FreshInputPerRun tests what each execution of a command decodes.
//
// Why this test is important:
//   - A command executed more than once (a test, a REPL) must not carry one run's
//     positional arguments into the next; a flag the caller adds to the returned
//     command (an output format) must keep working on every execution; and two commands
//     built from one registry must not share a run's input.
//
// What it tests:
//   - A reused command given `api x y` and then `web` runs the second with Target "web"
//     and no Rest; a consumer-added flag parses on both executions; commands built
//     separately for `--fix --only a` and `--only b` decode exactly their own flags.
func TestTransportCobra_FreshInputPerRun(t *testing.T) {
	t.Parallel()
	var got cobraInput
	reg := cobraRegistry(t, &got)
	cmd, err := opscobra.Command(reg, "check", opscobra.Options{})
	require.NoError(t, err)
	var jsonOut bool
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print JSON")
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	for _, args := range [][]string{
		{"-e", "s", "--json", "api", "x", "y"},
		{"-e", "s", "--json", "web"},
	} {
		cmd.SetArgs(args)
		require.NoError(t, cmd.ExecuteContext(t.Context()))
	}
	assert.True(t, jsonOut)
	assert.Equal(t, cobraInput{Env: "s", Target: "web"}, got)

	require.NoError(t, execCobra(t, reg, "-e", "s", "--fix", "--only", "a", "api"))
	require.NoError(t, execCobra(t, reg, "-e", "s", "--only", "b", "web"))
	assert.Equal(t, cobraInput{Env: "s", Only: []string{"b"}, Target: "web"}, got)
}

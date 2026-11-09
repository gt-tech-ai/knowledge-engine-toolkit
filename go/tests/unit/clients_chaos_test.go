package unit_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/chaos"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestChaosFromConfig tests the injector factory's kinds.
//
// Why this test is important:
//   - The injector is selected by config; the default must boot with no cluster, and a
//     typo'd kind must stop the run before any fault rather than fall back silently.
//
// What it tests:
//   - The empty kind is the stub: Inject and Revert succeed and Active is empty, with
//     no kubectl call. An unknown kind is INVALID_INPUT naming it and the known kinds;
//     chaos-mesh with no runner is INVALID_INPUT.
func TestChaosFromConfig(t *testing.T) {
	t.Parallel()

	stub, err := chaos.NewFromConfig(t.Context(), chaos.Config{},
		chaos.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)

	f := types.Fault{Name: textF, Spec: types.FaultSpec{Type: types.FaultPodKill}}
	require.NoError(t, stub.Inject(t.Context(), &f))
	require.NoError(t, stub.Revert(t.Context(), &f))

	active, err := stub.Active(t.Context(), "ns")
	require.NoError(t, err)
	assert.Empty(t, active)

	_, err = chaos.NewFromConfig(t.Context(), chaos.Config{Kind: "fis"},
		chaos.Deps{Decorations: fixtures.Decorations()})
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), textGotV, err)
	assert.Contains(t, err.Error(), "fis")
	assert.Contains(t, err.Error(), "chaos-mesh")

	_, err = chaos.NewFromConfig(t.Context(), chaos.Config{Kind: chaos.KindChaosMesh},
		chaos.Deps{Decorations: fixtures.Decorations()})
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), textGotV, err)
}

// TestChaosMeshInjector_CreatesAndDeletesTheFaultResource tests the Chaos Mesh
// injector's contract with the cluster: the resource it creates and the delete that
// reverts it.
//
// Why this test is important:
//   - The resource is the blast radius: its namespace and label selectors decide
//     which pods are hit, and its duration bounds the fault if the runner dies. A
//     wrong selector or a revert that doesn't wait would hit or keep hurting the
//     wrong thing.
//
// What it tests:
//   - For each fault type, Inject runs `kubectl create -f <file>` whose manifest is
//     the expected chaos-mesh.org/v1alpha1 kind in the target namespace, selecting the
//     service's pods by app.kubernetes.io/name (and, for network faults, the toward
//     service's pods as the destination), with the fault's parameters and duration;
//     Revert runs `kubectl delete <kind>.chaos-mesh.org <name> --namespace <ns>
//     --ignore-not-found --wait=true`.
func TestChaosMeshInjector_CreatesAndDeletesTheFaultResource(t *testing.T) {
	t.Parallel()

	cases := chaosFaultResourceTable()
	for _, tc := range cases {
		t.Run(string(tc.fault.Type), func(t *testing.T) {
			t.Parallel()

			testChaosMeshInjectorCreatesAndDeletesTheFaultResourceCase(t, &tc)
		})
	}
}

// testChaosMeshInjectorCreatesAndDeletesTheFaultResourceCase is the body of a closure in
// TestChaosMeshInjector_CreatesAndDeletesTheFaultResource.
func testChaosMeshInjectorCreatesAndDeletesTheFaultResourceCase(
	t *testing.T,
	tc *chaosFaultResourceRow,
) {
	t.Helper()

	runner := mocks.NewMockCommandRunner(gomock.NewController(t))
	fault := types.Fault{
		Name: "exp-20260925-101500",
		Target: types.FaultTarget{
			Namespace: textAcmeApp,
			Service:   textAPI,
		},
		Spec:     tc.fault,
		Duration: minutes2 * time.Minute,
	}

	var chaosManifest []byte

	testChaosMeshInjectorCreatesAndDeletesTheFaultResourceRest(
		t,
		tc,
		runner,
		&fault,
		chaosManifest,
	)
}

// testChaosMeshInjectorCreatesAndDeletesTheFaultResourceRest continues
// testChaosMeshInjectorCreatesAndDeletesTheFaultResourceCase with its remaining steps.
func testChaosMeshInjectorCreatesAndDeletesTheFaultResourceRest(
	t *testing.T,
	tc *chaosFaultResourceRow,
	runner *mocks.MockCommandRunner,
	fault *types.Fault,
	chaosManifest []byte,
) {
	t.Helper()

	runner.EXPECT().
		RunBuffered(gomock.Any(), textRoot, textKubectl, textCreate, "-f", gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			_, _ string,
			args ...string,
		) interfaces.CmdResult {
			data, err := os.ReadFile(args[2])
			require.NoError(t, err)

			chaosManifest = data

			return interfaces.CmdResult{}
		})
	runner.EXPECT().
		RunBuffered(gomock.Any(), textRoot, textKubectl, textDelete, tc.resource,
			fault.Name, "--namespace", textAcmeApp, "--ignore-not-found", "--wait=true").
		Return(interfaces.CmdResult{})

	inj, err := chaos.NewFromConfig(
		t.Context(),
		chaos.Config{Kind: chaos.KindChaosMesh, ManagedBy: textAcmeChaos},
		chaos.Deps{
			Decorations: fixtures.Decorations(),
			Runner:      runner,
			Root:        textRoot,
		},
	)
	require.NoError(t, err)
	require.NoError(t, inj.Inject(t.Context(), fault))
	require.NoError(t, inj.Revert(t.Context(), fault))

	var got map[string]any

	require.NoError(t, json.Unmarshal(chaosManifest, &got))
	assert.Equal(t, "chaos-mesh.org/v1alpha1", got["apiVersion"])
	assert.Equal(t, tc.kind, got[textKind])
	assert.Equal(t, map[string]any{
		textName:    fault.Name,
		"namespace": textAcmeApp,
		"labels": map[string]any{
			"app.kubernetes.io/managed-by": textAcmeChaos,
		},
	}, got["metadata"])
	assert.Equal(t, tc.wantSpec, got["spec"])
}

// chaosFaultResourceRow is one row of
// chaosFaultResourceTable.
type chaosFaultResourceRow struct {
	wantSpec map[string]any
	kind     string
	resource string
	fault    types.FaultSpec
}

// chaosFaultResourceTable builds the table
// TestChaosMeshInjector_CreatesAndDeletesTheFaultResource runs.
func chaosFaultResourceTable() []chaosFaultResourceRow {
	return []chaosFaultResourceRow{
		{
			fault:    types.FaultSpec{Type: types.FaultPodKill},
			kind:     "PodChaos",
			resource: "podchaos.chaos-mesh.org",
			wantSpec: map[string]any{
				textAction:   "pod-kill",
				textMode:     textOne,
				textSelector: chaosSelector(textAPI),
				textDuration: text2m0s,
			},
		},
		{
			fault: types.FaultSpec{
				Type:    types.FaultNetworkLatency,
				Toward:  textRetrieval,
				Latency: millis300 * time.Millisecond,
			},
			kind:     "NetworkChaos",
			resource: "networkchaos.chaos-mesh.org",
			wantSpec: map[string]any{
				textAction:   "delay",
				textMode:     textAll,
				textSelector: chaosSelector(textAPI),
				"direction":  "to",
				textTarget: map[string]any{
					textMode:     textAll,
					textSelector: chaosSelector(textRetrieval),
				},
				"delay":      map[string]any{"latency": "300ms"},
				textDuration: text2m0s,
			},
		},
		{
			fault: types.FaultSpec{
				Type:        types.FaultNetworkLoss,
				Toward:      textRetrieval,
				LossPercent: lossPercent10,
			},
			kind:     "NetworkChaos",
			resource: "networkchaos.chaos-mesh.org",
			wantSpec: map[string]any{
				textAction:   textLoss,
				textMode:     textAll,
				textSelector: chaosSelector(textAPI),
				"direction":  "to",
				textTarget: map[string]any{
					textMode:     textAll,
					textSelector: chaosSelector(textRetrieval),
				},
				textLoss:     map[string]any{textLoss: "10"},
				textDuration: text2m0s,
			},
		},
	}
}

// TestChaosMeshInjector_InjectFailureCarriesKubectlStderr tests that a rejected
// create surfaces kubectl's reason.
//
// Why this test is important:
//   - The first live run fails here when the CRDs or RBAC are missing; the error must
//     say why, not just "exit status 1".
//
// What it tests:
//   - When `kubectl create` fails, Inject returns an error containing its stderr.
func TestChaosMeshInjector_InjectFailureCarriesKubectlStderr(t *testing.T) {
	t.Parallel()

	runner := mocks.NewMockCommandRunner(gomock.NewController(t))
	runner.EXPECT().
		RunBuffered(gomock.Any(), ".", textKubectl, textCreate, "-f", gomock.Any()).
		Return(interfaces.CmdResult{
			Err:    apperr.New(apperr.CodeInternal, "exit status 1"),
			Stderr: []byte("forbidden: cannot create podchaos\n"),
		})

	inj, err := chaos.NewFromConfig(t.Context(),
		chaos.Config{Kind: chaos.KindChaosMesh, ManagedBy: textAcmeChaos},
		chaos.Deps{Decorations: fixtures.Decorations(), Runner: runner, Root: "."})
	require.NoError(t, err)

	err = inj.Inject(t.Context(), &types.Fault{
		Name:   "exp-1",
		Target: types.FaultTarget{Namespace: textAcmeApp, Service: textAPI},
		Spec:   types.FaultSpec{Type: types.FaultPodKill},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden: cannot create podchaos")
}

// chaosSelector is the decoded Chaos Mesh selector for service's pods in acme-app.
func chaosSelector(service string) map[string]any {
	return map[string]any{
		"namespaces":     []any{textAcmeApp},
		"labelSelectors": map[string]any{"app.kubernetes.io/name": service},
	}
}

// TestChaosMeshInjector_CleansUpAndFailsLoudly tests the injector's temp-file cleanup
// and its revert and unknown-fault errors.
//
// Why this test is important:
//   - The rendered manifest is written to a temp file each run; leaking it litters the
//     runner. A failed revert leaves a live fault, so its error must carry kubectl's
//     reason; an unknown fault type must stop before kubectl runs.
//
// What it tests:
//   - After Inject, the `chaos-*.json` manifest kubectl was given no longer exists.
//   - A failing `kubectl delete` makes Revert return an error carrying its stderr.
//   - Inject and Revert of an unknown fault type return INVALID_INPUT without a kubectl
//     call (the strict mock would fail the test).
func TestChaosMeshInjector_CleansUpAndFailsLoudly(t *testing.T) {
	t.Parallel()

	fault := types.Fault{
		Name:   "exp-1",
		Target: types.FaultTarget{Namespace: textAcmeApp, Service: textAPI},
		Spec:   types.FaultSpec{Type: types.FaultPodKill},
	}
	newInj := func(r interfaces.CommandRunner) interfaces.FaultInjector {
		inj, err := chaos.NewFromConfig(t.Context(),
			chaos.Config{Kind: chaos.KindChaosMesh, ManagedBy: textAcmeChaos},
			chaos.Deps{Decorations: fixtures.Decorations(), Runner: r, Root: "."})
		require.NoError(t, err)

		return inj
	}

	runner := mocks.NewMockCommandRunner(gomock.NewController(t))

	var appliedManifest string

	runner.EXPECT().
		RunBuffered(gomock.Any(), ".", textKubectl, textCreate, "-f", gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, a ...string) interfaces.CmdResult {
			appliedManifest = a[2]

			return interfaces.CmdResult{}
		})
	runner.EXPECT().RunBuffered(gomock.Any(), ".", textKubectl, textDelete, gomock.Any(),
		gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(interfaces.CmdResult{
			Err: apperr.New(apperr.CodeInternal, "exit status 1"),
			Stderr: []byte(
				"error: the server has asked for the client to provide credentials\n",
			),
		})

	inj := newInj(runner)

	require.NoError(t, inj.Inject(t.Context(), &fault))
	assert.True(
		t,
		strings.HasPrefix(
			appliedManifest[strings.LastIndex(appliedManifest, textSlash)+1:],
			"chaos-",
		),
	)

	_, err := os.Stat(appliedManifest)

	testChaosMeshInjectorCleansUpAndFailsLoudlyRest(t, &fault, newInj, inj, err)
}

// testChaosMeshInjectorCleansUpAndFailsLoudlyRest continues
// TestChaosMeshInjector_CleansUpAndFailsLoudly with its remaining steps.
func testChaosMeshInjectorCleansUpAndFailsLoudlyRest(
	t *testing.T,
	fault *types.Fault,
	newInj func(r interfaces.CommandRunner) interfaces.FaultInjector,
	inj interfaces.FaultInjector,
	err error,
) {
	t.Helper()

	require.ErrorIs(
		t,
		err,
		os.ErrNotExist,
		"the temp appliedManifest is removed after Inject",
	)

	err = inj.Revert(t.Context(), fault)
	require.Error(t, err)
	assert.Contains(
		t,
		err.Error(),
		"the server has asked for the client to provide credentials",
	)

	unknown := *fault

	unknown.Spec.Type = "cpu-burn"

	strict := newInj(mocks.NewMockCommandRunner(gomock.NewController(t)))

	for _, err := range []error{
		strict.Inject(t.Context(), &unknown), strict.Revert(t.Context(), &unknown),
	} {
		require.Error(t, err)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), textV2, err)
	}
}

package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/gate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// granularCollab is a set of observability collaborators, each a distinct mock, so a
// Deps shows which of them a component got.
func granularCollab(t *testing.T) decorate.Collaborators {
	t.Helper()
	ctrl := gomock.NewController(t)
	return decorate.Collaborators{Observability: decorate.Observability{
		Logger:  mocks.NewMockLogger(ctrl),
		Metrics: mocks.NewMockMetrics(ctrl),
		Tracer:  mocks.NewMockTracer(ctrl),
	}}
}

// TestDecorationTierFor_CascadesOverridesAlongTheNamesDots tests override resolution.
//
// Why this test is important:
//   - A deployable has hundreds of components; one key must be able to cover an app or
//     a resource and a deeper key one component, or the YAML either repeats itself or
//     cannot reach a single stage.
//
// What it tests:
//   - "api.documents.create" gets the defaults, then the "api" override, then
//     "api.documents", then its own, each deeper one winning where it sets a value.
//   - A sibling ("api.users.create") gets only the defaults and the "api" override.
func TestDecorationTierFor_CascadesOverridesAlongTheNamesDots(t *testing.T) {
	t.Parallel()
	on, off := true, false
	two, five := 2*time.Second, 5*time.Second
	tier := decoration.Tier{
		Defaults: decoration.Layers{Logging: &off, Tracing: &off},
		Overrides: map[string]decoration.Layers{
			"api":                  {Logging: &on, Timeout: &five},
			"api.documents":        {Tracing: &on},
			"api.documents.create": {Timeout: &two},
		},
	}

	create := tier.For("api.documents.create")
	assert.True(t, decoration.On(create.Logging), "from api")
	assert.True(t, decoration.On(create.Tracing), "from api.documents")
	require.NotNil(t, create.Timeout)
	assert.Equal(t, two, *create.Timeout, "its own wins over api's")

	sibling := tier.For("api.users.create")
	assert.True(t, decoration.On(sibling.Logging))
	assert.False(t, decoration.On(sibling.Tracing))
	require.NotNil(t, sibling.Timeout)
	assert.Equal(t, five, *sibling.Timeout)
}

// TestDecorations_RefusesAnOverrideTurningOnALayerItsTierLacks tests the layer check.
//
// Why this test is important:
//   - A tier runs only the layers it orders: retry on a workflow, or a breaker on a
//     store, would be accepted and then do nothing, so the operator believes a
//     protection is in place that is not.
//
// What it tests:
//   - An override that enables retry on a workflow, or a circuit breaker on a store, is
//     INVALID_INPUT naming the component and the layer.
//   - An override that turns such a layer off, and one that enables a layer the tier
//     orders, resolve.
func TestDecorations_RefusesAnOverrideTurningOnALayerItsTierLacks(t *testing.T) {
	t.Parallel()
	on, off := true, false
	cfg := decoration.DefaultConfig()
	cfg.Workflows.Overrides = map[string]decoration.Layers{
		"api.ask":   {Retry: decoration.Retry{Enabled: &on}},
		"api.quiet": {Retry: decoration.Retry{Enabled: &off}, Logging: &off},
	}
	cfg.Stores.Overrides = map[string]decoration.Layers{
		"api.documents": {CircuitBreaker: decoration.CircuitBreaker{Enabled: &on}},
	}
	cfg.Repositories.Overrides = map[string]decoration.Layers{
		"api.documents": {CircuitBreaker: decoration.CircuitBreaker{Enabled: &on}},
	}
	decs := decorate.NewDecorations(&cfg, granularCollab(t))

	_, err := decs.Deps(workflow.Tier(), "api.ask")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "api.ask")
	require.ErrorContains(t, err, "retry")
	_, err = decs.Deps(store.Tier(), "api.documents")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "circuit_breaker")

	_, err = decs.Deps(workflow.Tier(), "api.quiet")
	require.NoError(t, err)
	repo, err := decs.Deps(repository.Tier(), "api.documents")
	require.NoError(t, err)
	assert.NotNil(t, repo.Breaker)
}

// TestDecorationsVerify_FailsOnAnOverrideNoComponentUses tests the boot-time key check.
//
// Why this test is important:
//   - A misspelled or stale override configures nothing and says nothing; the
//     component it meant keeps its defaults in production.
//   - Several deployables share one config file, so a deployable may judge only the
//     overrides under its own name.
//
// What it tests:
//   - After the api resolves "api.documents" (repository) and "api.documents.create"
//     (pipeline), Verify("api") fails INVALID_INPUT naming the unused
//     "api.documnets" override and its tier, but not "identity.users".
//   - The "api" pipeline override counts as used by "api.documents.create".
//   - With the stray key gone, Verify("api") passes; a decoration that failed to
//     resolve through Subject is reported by Verify.
func TestDecorationsVerify_FailsOnAnOverrideNoComponentUses(t *testing.T) {
	t.Parallel()
	on := true
	cfg := decoration.DefaultConfig()
	cfg.Repositories.Overrides = map[string]decoration.Layers{
		"api.documents":  {Tracing: &on},
		"api.documnets":  {Tracing: &on},
		"identity.users": {Tracing: &on},
	}
	cfg.Pipelines.Overrides = map[string]decoration.Layers{"api": {Logging: &on}}
	decs := decorate.NewDecorations(&cfg, granularCollab(t))
	_, err := decs.Deps(repository.Tier(), "api.documents")
	require.NoError(t, err)
	decs.Subject(pipeline.Tier(), "api.documents.create")

	err = decs.Verify("api")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "repositories.overrides.api.documnets")
	assert.NotContains(t, err.Error(), "identity.users")
	assert.NotContains(t, err.Error(), "pipelines.overrides.api")

	delete(cfg.Repositories.Overrides, "api.documnets")
	clean := decorate.NewDecorations(&cfg, granularCollab(t))
	_, err = clean.Deps(repository.Tier(), "api.documents")
	require.NoError(t, err)
	clean.Subject(pipeline.Tier(), "api.documents.create")
	require.NoError(t, clean.Verify("api"))

	cfg.Workflows.Overrides = map[string]decoration.Layers{
		"api.ask": {Retry: decoration.Retry{Enabled: &on}},
	}
	broken := decorate.NewDecorations(&cfg, granularCollab(t))
	broken.Subject(workflow.Tier(), "api.ask")
	err = broken.Verify("api")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "retry")
}

// documentsPipeline is a pipeline contract with two stages, create and delete.
type documentsPipeline interface {
	// Create runs the create stage.
	Create(ctx context.Context, in string) (string, error)

	// Delete runs the delete stage.
	Delete(ctx context.Context, in string) (string, error)
}

// TestConfiguredBases_ResolveEachStageJobAndGateByItsOwnName tests the bases of the
// pipeline, job and gate tiers.
//
// Why this test is important:
//   - A pipeline's stages, a worker's jobs and its gate are separate units of work;
//     resolving them under one shared name makes them share one decoration, so none can
//     be tuned on its own.
//
// What it tests:
//   - A stage of a pipeline built from its contract as "api.documents" is logged when
//     the "api.documents.create" override turns logging on, and its sibling stage is
//     not.
//   - A job built as "events.sweep" runs as its "execute" operation, and a gate
//     decorated from decs runs as "run" under its own name in the gate tier.
//   - Each name counts as used for Verify.
func TestConfiguredBases_ResolveEachStageJobAndGateByItsOwnName(t *testing.T) {
	t.Parallel()
	on := true
	cfg := decoration.DefaultConfig()
	cfg.Pipelines.Overrides = map[string]decoration.Layers{
		"api.documents.create": {Logging: &on},
	}
	cfg.Jobs.Overrides = map[string]decoration.Layers{"events.sweep": {Logging: &on}}
	cfg.Gates.Overrides = map[string]decoration.Layers{"events.nightly": {Logging: &on}}
	ctrl := gomock.NewController(t)
	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	var logged []string
	// The logging layer logs each operation at Debug as (tier, name, "operation", op).
	logger.EXPECT().Debug(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ string, kv ...any) {
			logged = append(logged, kv[1].(string)+"."+kv[3].(string))
		}).AnyTimes()
	decs := decorate.NewDecorations(&cfg, decorate.Collaborators{
		Observability: decorate.Observability{Logger: logger},
	})

	pipes, err := pipeline.NewBase[documentsPipeline](decs, "api.documents")
	require.NoError(t, err)
	identity := func(_ context.Context, in string) (string, error) { return in, nil }
	_, err = pipes.Stage("create", identity).Execute(t.Context(), "x")
	require.NoError(t, err)
	_, err = pipes.Stage("delete", identity).Execute(t.Context(), "x")
	require.NoError(t, err)

	jobs, err := job.NewBase(decs, "events.sweep")
	require.NoError(t, err)
	sweep := jobs.Local(
		"sweep",
		"events",
		func(context.Context) (types.StepResults, error) { return nil, nil },
	)
	require.NoError(t, gate.New("events.nightly").
		Serial("sweep", sweep).
		Decorate(decs).
		RunLocal(t.Context()))

	assert.Contains(t, logged, "api.documents.create")
	assert.NotContains(t, logged, "api.documents.delete")
	assert.Contains(t, logged, "events.sweep.execute")
	assert.Contains(t, logged, "events.nightly.run")
	require.NoError(t, decs.Verify("api", "events"))
	assert.Equal(t, "gate", gate.Tier().Name)
}

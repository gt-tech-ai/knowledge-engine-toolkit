package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/gate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// granularCollab is a set of observability collaborators, each a distinct mock, so a
// Deps shows which of them a component got.
func granularCollab(t *testing.T) decorate.Collaborators {
	t.Helper()

	ctrl := gomock.NewController(t)

	return decorate.Collaborators{
		Logger:  mocks.NewMockLogger(ctrl),
		Metrics: mocks.NewMockMetrics(ctrl),
		Tracer:  mocks.NewMockTracer(ctrl),
		Auth:    nil,
	}
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

	enabled, off := true, false
	two, five := seconds*time.Second, secondsDots*time.Second
	tier := decoration.Tier{
		Defaults: fixtures.Build(func(row *decoration.Layers) {
			row.Logging = &off
			row.Tracing = &off
		}),
		Overrides: map[string]decoration.Layers{
			textAPI: fixtures.Build(func(row *decoration.Layers) {
				row.Logging = &enabled
				row.Timeout = &five
			}),
			textAPIDocuments: fixtures.Build(func(row *decoration.Layers) {
				row.Tracing = &enabled
			}),
			textAPIDocumentsCreate: fixtures.Build(func(row *decoration.Layers) {
				row.Timeout = &two
			}),
		},
	}

	create := tier.For(textAPIDocumentsCreate)
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

	enabled, off := true, false
	cfg := decoration.DefaultConfig()

	cfg.Workflows.Overrides = map[string]decoration.Layers{
		textAPIAsk: fixtures.Build(func(row *decoration.Layers) {
			row.Retry = decoration.Retry{
				Enabled:     &enabled,
				RetryConfig: stdx.Zero[resilience.RetryConfig](),
			}
		}),
		"api.quiet": fixtures.Build(func(row *decoration.Layers) {
			row.Retry = decoration.Retry{
				Enabled:     &off,
				RetryConfig: stdx.Zero[resilience.RetryConfig](),
			}
			row.Logging = &off
		}),
	}
	cfg.Stores.Overrides = map[string]decoration.Layers{
		textAPIDocuments: fixtures.Build(func(row *decoration.Layers) {
			row.CircuitBreaker = decoration.CircuitBreaker{
				Enabled:       &enabled,
				BreakerConfig: stdx.Zero[resilience.BreakerConfig](),
			}
		}),
	}
	cfg.Repositories.Overrides = map[string]decoration.Layers{
		textAPIDocuments: fixtures.Build(func(row *decoration.Layers) {
			row.CircuitBreaker = decoration.CircuitBreaker{
				Enabled:       &enabled,
				BreakerConfig: stdx.Zero[resilience.BreakerConfig](),
			}
		}),
	}

	decs := decorate.NewDecorations(&cfg, granularCollab(t))

	_, err := decs.Deps(workflow.Tier(), textAPIAsk)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, textAPIAsk)
	require.ErrorContains(t, err, "retry")

	_, err = decs.Deps(store.Tier(), textAPIDocuments)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "circuit_breaker")

	_, err = decs.Deps(workflow.Tier(), "api.quiet")
	require.NoError(t, err)

	repo, err := decs.Deps(repository.Tier(), textAPIDocuments)
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

	enabled := true
	cfg := decoration.DefaultConfig()

	cfg.Repositories.Overrides = map[string]decoration.Layers{
		textAPIDocuments: fixtures.Build(func(row *decoration.Layers) {
			row.Tracing = &enabled
		}),
		"api.documnets": fixtures.Build(func(row *decoration.Layers) {
			row.Tracing = &enabled
		}),
		"identity.users": fixtures.Build(func(row *decoration.Layers) {
			row.Tracing = &enabled
		}),
	}
	cfg.Pipelines.Overrides = map[string]decoration.Layers{
		textAPI: fixtures.Build(func(row *decoration.Layers) {
			row.Logging = &enabled
		}),
	}

	decs := decorate.NewDecorations(&cfg, granularCollab(t))
	_, err := decs.Deps(repository.Tier(), textAPIDocuments)
	require.NoError(t, err)
	decs.Subject(pipeline.Tier(), textAPIDocumentsCreate)

	err = decs.Verify(textAPI)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "repositories.overrides.api.documnets")
	assert.NotContains(t, err.Error(), "identity.users")
	assert.NotContains(t, err.Error(), "pipelines.overrides.api")

	delete(cfg.Repositories.Overrides, "api.documnets")

	clean := decorate.NewDecorations(&cfg, granularCollab(t))

	_, err = clean.Deps(repository.Tier(), textAPIDocuments)
	require.NoError(t, err)
	clean.Subject(pipeline.Tier(), textAPIDocumentsCreate)
	require.NoError(t, clean.Verify(textAPI))

	cfg.Workflows.Overrides = map[string]decoration.Layers{
		textAPIAsk: fixtures.Build(func(row *decoration.Layers) {
			row.Retry = decoration.Retry{
				Enabled:     &enabled,
				RetryConfig: stdx.Zero[resilience.RetryConfig](),
			}
		}),
	}

	broken := decorate.NewDecorations(&cfg, granularCollab(t))
	broken.Subject(workflow.Tier(), textAPIAsk)

	err = broken.Verify(textAPI)
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

	enabled := true
	cfg := decoration.DefaultConfig()

	cfg.Pipelines.Overrides = map[string]decoration.Layers{
		textAPIDocumentsCreate: fixtures.Build(func(row *decoration.Layers) {
			row.Logging = &enabled
		}),
	}
	cfg.Jobs.Overrides = map[string]decoration.Layers{
		"events.sweep": fixtures.Build(func(row *decoration.Layers) {
			row.Logging = &enabled
		}),
	}
	cfg.Gates.Overrides = map[string]decoration.Layers{
		"events.nightly": fixtures.Build(func(row *decoration.Layers) {
			row.Logging = &enabled
		}),
	}

	ctrl := gomock.NewController(t)
	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()

	var logged []string

	// The logging layer logs each operation at Debug as (tier, name, "operation", op).
	logger.EXPECT().Debug(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ string, kv ...any) {
			logged = append(logged, asType[string](t, kv[1])+"."+asType[string](t, kv[3]))
		}).AnyTimes()

	testConfiguredBasesResolveEachStageJobAndGateByItsOwnNameChecksNames(t,
		&cfg,
		logger,
		&logged,
	)
}

// testConfiguredBasesResolveEachStageJobAndGateByItsOwnNameChecksNames continues
// TestConfiguredBases_ResolveEachStageJobAndGateByItsOwnName.
func testConfiguredBasesResolveEachStageJobAndGateByItsOwnNameChecksNames(t *testing.T,
	cfg *decoration.Config,
	logger *mocks.MockLogger,
	logged *[]string,
) {
	t.Helper()

	decs := decorate.NewDecorations(
		cfg,
		fixtures.Build(func(row *decorate.Collaborators) {
			row.Logger = logger
		}),
	)

	pipes, err := pipeline.NewBase[documentsPipeline](decs, textAPIDocuments)
	require.NoError(t, err)

	identity := func(_ context.Context, in string) (string, error) { return in, nil }

	_, err = pipes.Stage(textCreate, identity).Execute(t.Context(), textX)
	require.NoError(t, err)

	_, err = pipes.Stage(textDelete, identity).Execute(t.Context(), textX)
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

	assert.Contains(t, (*logged), textAPIDocumentsCreate)
	assert.NotContains(t, (*logged), "api.documents.delete")
	assert.Contains(t, (*logged), "events.sweep.execute")
	assert.Contains(t, (*logged), "events.nightly.run")
	require.NoError(t, decs.Verify(textAPI, "events"))
	assert.Equal(t, textGate, gate.Tier().Name)
}

const (
	// secondsDots is the factor multiplying time.Second in
	// TestDecorationTierFor_CascadesOverridesAlongTheNamesDots.
	secondsDots = 5
)

const (
	// textAPIAsk is the literal "api.ask".
	textAPIAsk = "api.ask"

	// textAPIDocuments is the literal "api.documents".
	textAPIDocuments = "api.documents"

	// textAPIDocumentsCreate is the literal "api.documents.create".
	textAPIDocumentsCreate = "api.documents.create"
)

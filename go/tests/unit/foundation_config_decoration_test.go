package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// TestLoadDecorationConfig_OverlaysTheSectionOnTheDefaults tests the decoration-config
// loader.
//
// Why this test is important:
//   - An absent section must keep today's decoration, a partial one must change only
//     what it names (turning on a client's retry must not drop its timeout), and a
//     section that fails to decode must stop boot instead of silently dropping a layer.
//
// What it tests:
//   - An absent section yields DefaultConfig() without unmarshalling.
//   - A section that turns on the storage client's retry keeps its default 30 s timeout,
//     bulkhead and breaker, and leaves every other tier at its default.
//   - An unmarshal failure is wrapped as INVALID_INPUT.
func TestLoadDecorationConfig_OverlaysTheSectionOnTheDefaults(t *testing.T) {
	t.Parallel()

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("decoration").Return(nil)

		cfg, err := decoration.Load(loader)

		require.NoError(t, err)
		assert.Equal(t, decoration.DefaultConfig(), cfg)
	})

	t.Run("partial", func(t *testing.T) {
		t.Parallel()
		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("decoration").Return(map[string]any{"clients": nil})
		loader.EXPECT().UnmarshalKey("decoration", gomock.Any()).DoAndReturn(
			func(_ string, target any) error {
				on := true
				target.(*decoration.Config).Clients.Overrides = map[string]decoration.Layers{
					"storage": {Retry: decoration.Retry{Enabled: &on}},
				}
				return nil
			},
		)

		cfg, err := decoration.Load(loader)

		require.NoError(t, err)
		storage := cfg.Clients.For("storage")
		assert.True(t, decoration.On(storage.Retry.Enabled))
		assert.True(t, decoration.On(storage.Bulkhead.Enabled))
		assert.True(t, decoration.On(storage.CircuitBreaker.Enabled))
		require.NotNil(t, storage.Timeout)
		assert.Equal(t, 30*time.Second, *storage.Timeout)
		assert.Equal(t, decoration.DefaultConfig().Services, cfg.Services)
	})

	t.Run("unmarshal failure", func(t *testing.T) {
		t.Parallel()
		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("decoration").Return(map[string]any{"clients": "maybe"})
		loader.EXPECT().
			UnmarshalKey("decoration", gomock.Any()).
			Return(apperr.Sentinel("bad layers"))

		_, err := decoration.Load(loader)

		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	})
}

// TestLoadDecorationConfig_KeepsDottedComponentNames tests decoration.Load over the
// layered YAML loader.
//
// Why this test is important:
//   - Components are named with dots ("outbox.sqs", "seed.ingress.gateway"), and the
//     YAML loader splits a dotted key into nested maps; an override under such a name
//     would otherwise decode as an empty override of its first segment and be dropped
//     without a word.
//
// What it tests:
//   - An override keyed "seed.ingress.gateway" in base.yaml, re-tuned in the env
//     overlay, resolves under its full name with both files' settings.
//   - A dotted engine default ("outbox.sqs") overridden in YAML keeps its other
//     defaults, and a name that is both a component and a prefix ("outbox" with
//     "outbox.s3" beneath it) resolves each.
func TestLoadDecorationConfig_KeepsDottedComponentNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	write("base.yaml", `decoration:
  clients:
    overrides:
      seed.ingress.gateway:
        timeout: 7s
        retry:
          enabled: true
      outbox.sqs:
        timeout: 5s
      outbox:
        logging: false
        s3:
          timeout: 4s
`)
	write("dev.yaml", `decoration:
  clients:
    overrides:
      seed.ingress.gateway:
        timeout: 9s
`)
	loader, err := config.New(
		config.KindViper,
		config.WithBaseDir(dir),
		config.WithEnvironment("dev"),
	)
	require.NoError(t, err)

	cfg, err := decoration.Load(loader)
	require.NoError(t, err)

	gw := cfg.Clients.For("seed.ingress.gateway")
	require.NotNil(t, gw.Timeout)
	assert.Equal(t, 9*time.Second, *gw.Timeout)
	assert.True(t, decoration.On(gw.Retry.Enabled))
	sqs := cfg.Clients.For("outbox.sqs")
	require.NotNil(t, sqs.Timeout)
	assert.Equal(t, 5*time.Second, *sqs.Timeout)
	assert.True(t, decoration.On(sqs.Bulkhead.Enabled), "keeps the engine default")
	assert.False(t, decoration.On(cfg.Clients.For("outbox").Logging))
	s3 := cfg.Clients.For("outbox.s3")
	require.NotNil(t, s3.Timeout)
	assert.Equal(t, 4*time.Second, *s3.Timeout)
	assert.NotContains(t, cfg.Clients.Overrides, "seed")
}

// TestDecorations_BuildEachComponentsLayersFromConfig tests decorate.Decorations.
//
// Why this test is important:
//   - Every decorated component — client, workflow, service, repository — takes its
//     layers from this one builder; a layer the config turns on must get its
//     collaborator or primitive, and one it leaves off must not, or a request is
//     traced at every tier or a client loses its breaker.
//
// What it tests:
//   - Under DefaultConfig a client and a workflow are observed, a service is not; the
//     storage client gets its 30 s timeout, a bulkhead at the default concurrency
//     limit and a breaker, and no retrier; a
//     repository gets a retrier and a breaker; every tier keeps recovery.
//   - A controller has no bulkhead or rate limit by default; turning its bulkhead on
//     makes it shed (the storage client's waits).
//   - An override turning a service's tracing and recovery off and its timeout on is
//     applied to that service only.
//   - A tier the config does not know, and an invalid bulkhead kind, are
//     INVALID_INPUT.
func TestDecorations_BuildEachComponentsLayersFromConfig(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	collab := decorate.Collaborators{Observability: decorate.Observability{
		Logger:  mocks.NewMockLogger(ctrl),
		Metrics: mocks.NewMockMetrics(ctrl),
		Tracer:  mocks.NewMockTracer(ctrl),
	}}
	defaults := decoration.DefaultConfig()
	decs := decorate.NewDecorations(&defaults, collab)
	deps := func(tier decorate.Tier, name string) decorate.Deps {
		d, err := decs.Deps(tier, name)
		require.NoError(t, err)
		return d
	}

	lock := deps(client.Tier(), "lock")
	assert.Equal(t, collab.Observability, lock.Observability)
	assert.Nil(t, lock.Breaker)
	assert.Equal(t, collab.Observability, deps(workflow.Tier(), "ask").Observability)

	svc := deps(service.Tier(), "users")
	assert.Equal(t, decorate.Observability{}, svc.Observability)
	assert.False(t, svc.SkipRecovery)

	storage := deps(client.Tier(), "storage")
	assert.Equal(t, 30*time.Second, storage.Timeout)
	require.NotNil(t, storage.Bulkhead)
	assertBulkheadLimit(
		t,
		storage.Bulkhead,
		resilience.DefaultBulkheadConfig().MaxConcurrent,
	)
	assert.NotNil(t, storage.Breaker)
	assert.Nil(t, storage.Retrier)
	assert.False(t, storage.ShedLoad)
	api := deps(transport.Tier(), "api")
	assert.Nil(t, api.Bulkhead)
	assert.Nil(t, api.RateLimiter)

	repo := deps(repository.Tier(), "users")
	assert.NotNil(t, repo.Retrier)
	assert.NotNil(t, repo.Breaker)

	off, on, timeout := false, true, 2*time.Second
	cfg := decoration.DefaultConfig()
	cfg.Services.Overrides = map[string]decoration.Layers{
		"billing": {Tracing: &off, Recovery: &off, Metrics: &on, Timeout: &timeout},
	}
	cfg.Controllers.Overrides = map[string]decoration.Layers{
		"api": {Bulkhead: decoration.Bulkhead{Enabled: &on}},
	}
	tuned := decorate.NewDecorations(&cfg, collab)
	billing, err := tuned.Deps(service.Tier(), "billing")
	require.NoError(t, err)
	assert.Nil(t, billing.Tracer)
	assert.NotNil(t, billing.Metrics)
	assert.True(t, billing.SkipRecovery)
	assert.Equal(t, timeout, billing.Timeout)
	shedding, err := tuned.Deps(transport.Tier(), "api")
	require.NoError(t, err)
	assert.NotNil(t, shedding.Bulkhead)
	assert.True(t, shedding.ShedLoad)
	users, err := tuned.Deps(service.Tier(), "users")
	require.NoError(t, err)
	assert.Equal(t, svc, users)

	_, err = decs.Deps(decorate.Tier{Name: "gateway"}, "x")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	bad := decoration.DefaultConfig()
	bad.Clients.Defaults.Bulkhead.Enabled = &on
	bad.Clients.Defaults.Bulkhead.Kind = "semaphore"
	_, err = decorate.NewDecorations(&bad, collab).Deps(client.Tier(), "lock")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestDecorations_RecoveryLogsAPanicWithLoggingOff tests that recovery keeps its logger
// when the logging layer is off.
//
// Why this test is important:
//   - Services are not logged by default; a panic there would otherwise be turned into
//     an internal error with its value and stack thrown away.
//
// What it tests:
//   - Under DefaultConfig a service has no logging layer, yet a panicking op is logged
//     at Error by the collaborators' logger and returns INTERNAL; with recovery off the
//     Deps carry no panic logger.
func TestDecorations_RecoveryLogsAPanicWithLoggingOff(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	logger := mocks.NewMockLogger(ctrl)
	logger.EXPECT().WithContext(gomock.Any()).Return(logger)
	logger.EXPECT().Error("service panic recovered", gomock.Any())
	defaults := decoration.DefaultConfig()
	collab := decorate.Collaborators{
		Observability: decorate.Observability{Logger: logger},
	}
	deps, err := decorate.NewDecorations(&defaults, collab).Deps(service.Tier(), "users")
	require.NoError(t, err)
	require.Nil(t, deps.Logger)

	_, err = service.Tier().Subject("users", deps).Exec(t.Context(), "Provision",
		func(context.Context) (int, error) { panic("boom") })
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))

	off := false
	unrecovered, err := decorate.Build("users", decoration.Layers{Recovery: &off}, collab)
	require.NoError(t, err)
	assert.Nil(t, unrecovered.PanicLogger)
}

// TestLoadDecorations_ReadsTheSectionWithTheCollaborators tests building Decorations
// straight from a config loader.
//
// Why this test is important:
//   - Every composition root turns the decoration section plus its observability into
//     Decorations; doing it in one engine call keeps each app's wiring to one provider.
//
// What it tests:
//   - An absent section yields DefaultConfig's layers with the collaborators attached
//     (a client is observed); a section that turns a client's tracing off is applied;
//     a section that fails to decode is INVALID_INPUT.
func TestLoadDecorations_ReadsTheSectionWithTheCollaborators(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	collab := decorate.Collaborators{Observability: decorate.Observability{
		Logger:  mocks.NewMockLogger(ctrl),
		Metrics: mocks.NewMockMetrics(ctrl),
		Tracer:  mocks.NewMockTracer(ctrl),
	}}

	absent := mocks.NewMockConfigLoader(ctrl)
	absent.EXPECT().Get(decoration.SectionKey).Return(nil)
	decs, err := decorate.LoadDecorations(absent, collab)
	require.NoError(t, err)
	search, err := decs.Deps(client.Tier(), "search")
	require.NoError(t, err)
	assert.Equal(t, collab.Observability, search.Observability)

	quiet := mocks.NewMockConfigLoader(ctrl)
	quiet.EXPECT().Get(decoration.SectionKey).Return(map[string]any{}).AnyTimes()
	quiet.EXPECT().UnmarshalKey(decoration.SectionKey, gomock.Any()).DoAndReturn(
		func(_ string, out any) error {
			off := false
			out.(*decoration.Config).Clients.Overrides = map[string]decoration.Layers{
				"search": {Tracing: &off},
			}
			return nil
		})
	decs, err = decorate.LoadDecorations(quiet, collab)
	require.NoError(t, err)
	search, err = decs.Deps(client.Tier(), "search")
	require.NoError(t, err)
	assert.Nil(t, search.Tracer)
	assert.NotNil(t, search.Logger)

	broken := mocks.NewMockConfigLoader(ctrl)
	broken.EXPECT().Get(decoration.SectionKey).Return(map[string]any{}).AnyTimes()
	broken.EXPECT().UnmarshalKey(decoration.SectionKey, gomock.Any()).
		Return(apperr.Sentinel("bad yaml"))
	_, err = decorate.LoadDecorations(broken, collab)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestLoadDecorationConfig_DefaultsOffAndReadsTheSection tests the decoration-config
// loader.
//
// Why this test is important:
//   - The switches pick which tiers below the request workflow are observed; an absent
//     section must leave every one off (each request observed once), and a section that
//     fails to decode must stop boot instead of silently dropping a switch.
//
// What it tests:
//   - An absent section yields DefaultConfig() (all off) without unmarshalling.
//   - A present section yields its values.
//   - An unmarshal failure is wrapped as INVALID_INPUT.
func TestLoadDecorationConfig_DefaultsOffAndReadsTheSection(t *testing.T) {
	t.Parallel()

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("decoration").Return(nil)

		cfg, err := decoration.Load(loader)

		require.NoError(t, err)
		assert.Equal(t, decoration.Config{}, cfg)
		assert.Equal(t, decoration.DefaultConfig(), cfg)
	})

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("decoration").Return(map[string]any{"services": true})
		loader.EXPECT().UnmarshalKey("decoration", gomock.Any()).DoAndReturn(
			func(_ string, target any) error {
				target.(*decoration.Config).Services = true
				return nil
			},
		)

		cfg, err := decoration.Load(loader)

		require.NoError(t, err)
		assert.Equal(t, decoration.Config{Services: true}, cfg)
	})

	t.Run("unmarshal failure", func(t *testing.T) {
		t.Parallel()
		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("decoration").Return(map[string]any{"services": "maybe"})
		loader.EXPECT().
			UnmarshalKey("decoration", gomock.Any()).
			Return(apperr.Sentinel("bad bool"))

		_, err := decoration.Load(loader)

		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	})
}

// TestNewTiers_ObservesOncePerRequestUnlessALowerTierIsOn tests how the per-tier
// observability bundles follow the decoration config.
//
// Why this test is important:
//   - A deployed service must log, meter and trace each request once, at its workflow;
//     a lower tier left on doubles every log line, metric series and span.
//   - A developer turns the lower tiers on to see where a request spends its time.
//
// What it tests:
//   - The workflow tier always carries the logger, metrics and tracer.
//   - Each lower tier carries them only when its switch is on, and is empty (so its
//     decorators are skipped) otherwise.
func TestNewTiers_ObservesOncePerRequestUnlessALowerTierIsOn(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	full := decorate.Observability{
		Logger:  mocks.NewMockLogger(ctrl),
		Metrics: mocks.NewMockMetrics(ctrl),
		Tracer:  mocks.NewMockTracer(ctrl),
	}
	none := decorate.Observability{}

	off := decorate.NewTiers(decoration.Config{}, full.Logger, full.Metrics, full.Tracer)
	assert.Equal(t, decorate.Tiers{Workflow: full}, off)

	on := decorate.NewTiers(
		decoration.Config{Pipelines: true, Services: true, Repositories: true},
		full.Logger, full.Metrics, full.Tracer,
	)
	assert.Equal(t, decorate.Tiers{
		Workflow: full, Pipeline: full, Service: full, Repository: full,
	}, on)

	servicesOnly := decorate.NewTiers(
		decoration.Config{Services: true}, full.Logger, full.Metrics, full.Tracer,
	)
	assert.Equal(t, decorate.Tiers{
		Workflow: full, Pipeline: none, Service: full, Repository: none,
	}, servicesOnly)
}

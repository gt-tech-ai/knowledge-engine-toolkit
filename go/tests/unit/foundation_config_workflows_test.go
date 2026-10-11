package unit_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	workflowscfg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/workflows"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestLoadWorkflowConfig_DefaultsWhenAbsentAndRejectsInvalid tests that the
// workflow-config loader overlays the "workflows" section on the defaults and
// rejects a section it cannot use.
//
// Why this test is important:
//   - Every composition root reads the workflow timeout through this loader; a
//     missing section must keep the safe default, and a bad one must stop boot
//     with a coded error instead of running with a nonsense timeout
//
// What it tests:
//   - An absent section yields DefaultConfig() without unmarshalling
//   - A present section with timeout 2s yields Timeout == 2s
//   - A negative timeout is rejected as INVALID_INPUT
//   - An unmarshal failure is wrapped as INVALID_INPUT
func TestLoadWorkflowConfig_DefaultsWhenAbsentAndRejectsInvalid(t *testing.T) {
	t.Parallel()

	setTimeout := func(d time.Duration) func(string, any) error {
		return func(_ string, target any) error {
			target.(*workflowscfg.Config).Timeout = d
			return nil
		}
	}

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("workflows").Return(nil)

		cfg, err := workflowscfg.Load(loader)

		require.NoError(t, err)
		assert.Equal(t, workflowscfg.DefaultConfig(), cfg)
	})

	t.Run("present", func(t *testing.T) {
		t.Parallel()
		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("workflows").Return(map[string]any{"timeout": "2s"})
		loader.EXPECT().
			UnmarshalKey("workflows", gomock.Any()).
			DoAndReturn(setTimeout(2 * time.Second))

		cfg, err := workflowscfg.Load(loader)

		require.NoError(t, err)
		assert.Equal(t, workflowscfg.Config{Timeout: 2 * time.Second}, cfg)
	})

	t.Run("negative timeout", func(t *testing.T) {
		t.Parallel()
		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("workflows").Return(map[string]any{"timeout": "-1s"})
		loader.EXPECT().
			UnmarshalKey("workflows", gomock.Any()).
			DoAndReturn(setTimeout(-time.Second))

		_, err := workflowscfg.Load(loader)

		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	})

	t.Run("unmarshal failure", func(t *testing.T) {
		t.Parallel()
		loader := mocks.NewMockConfigLoader(gomock.NewController(t))
		loader.EXPECT().Get("workflows").Return(map[string]any{"timeout": "soon"})
		loader.EXPECT().
			UnmarshalKey("workflows", gomock.Any()).
			Return(apperr.Sentinel("bad duration"))

		_, err := workflowscfg.Load(loader)

		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
		assert.Contains(t, err.Error(), "load workflows config")
	})
}

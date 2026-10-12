package unit_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestSection_DecodesAndValidates tests the typed section loader.
//
// Why this test is important:
//   - Every composition root extracts its typed config slices through it; a section
//     that skipped Validate would boot a service on an invalid config.
//
// What it tests:
//   - A decodable, valid section is returned as decoded.
//   - A decode failure and a validation failure are CodeInvalidInput.
func TestSection_DecodesAndValidates(t *testing.T) {
	t.Parallel()
	loader := mocks.NewMockConfigLoader(gomock.NewController(t))

	loader.EXPECT().UnmarshalKey("logging", gomock.Any()).DoAndReturn(
		func(_ string, target any) error {
			cfg, ok := target.(*infra.LoggingConfig)
			require.True(t, ok)
			*cfg = infra.DefaultLoggingConfig()
			return nil
		})
	got, err := config.Section[infra.LoggingConfig](loader, "logging")
	require.NoError(t, err)
	require.Equal(t, infra.DefaultLoggingConfig(), got)

	loader.EXPECT().UnmarshalKey("logging", gomock.Any()).
		Return(apperr.Sentinel("bad yaml"))
	_, err = config.Section[infra.LoggingConfig](loader, "logging")
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	loader.EXPECT().UnmarshalKey("logging", gomock.Any()).DoAndReturn(
		func(_ string, target any) error {
			cfg, ok := target.(*infra.LoggingConfig)
			require.True(t, ok)
			cfg.Level = "shout"
			return nil
		})
	_, err = config.Section[infra.LoggingConfig](loader, "logging")
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

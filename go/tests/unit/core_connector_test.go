package unit_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestConnectorSeams_MocksSatisfyContracts tests that the generated mocks for the
// connector-family read seam and its builder satisfy their core contracts.
//
// Why this test is important:
//   - ConnectorSource + SourceBuilder are the AWS-free, dependency-free core seams the
//     pkg connector family (S3 now; Salesforce/GDrive later) and the app connector
//     service depend on; their shape and their generated mocks must stay in lockstep so a
//     consumer can be unit-tested against the mock.
//
// What it tests:
//   - The mockgen-generated MockConnectorSource / MockSourceBuilder are assignable to
//     their core interfaces and construct without panic.
func TestConnectorSeams_MocksSatisfyContracts(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)

	var (
		src     interfaces.ConnectorSource = mocks.NewMockConnectorSource(ctrl)
		builder interfaces.SourceBuilder   = mocks.NewMockSourceBuilder(ctrl)
	)

	require.NotNil(t, src)
	require.NotNil(t, builder)
}

// TestSourceConfig_RedactsAppPrivateKey tests that formatting or marshaling a
// SourceConfig never emits the GitHub App's private key.
//
// Why this test is important:
//   - AppPrivateKey is an already-decrypted credential that mints installation tokens
//     for every repository the App can read. A config printed in an error, a log line,
//     a %#v debug dump or a JSON document must not leak it.
//
// What it tests:
//   - %v, %+v, %s and %#v of a SourceConfig (value and pointer) and its JSON encoding
//     show [REDACTED] instead of the key and still show the non-secret fields; the key
//     JSON-encodes as "[REDACTED]"; an empty key prints nothing.
func TestSourceConfig_RedactsAppPrivateKey(t *testing.T) {
	t.Parallel()

	cfg := fixtures.Build(func(c *interfaces.SourceConfig) {
		c.Kind = interfaces.ConnectorKindGitHub
		c.Repo = textWidgets
		c.AppPrivateKey = "-----BEGIN RSA PRIVATE KEY-----app-key-value"
	})

	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		for _, out := range []string{fmt.Sprintf(verb, cfg), fmt.Sprintf(verb, &cfg)} {
			require.NotContains(t, out, "app-key-value", verb)
			require.Contains(t, out, "[REDACTED]", verb)
			require.Contains(t, out, textWidgets, verb)
		}
	}

	encoded, err := json.Marshal(cfg.AppPrivateKey)
	require.NoError(t, err)
	require.JSONEq(t, `"[REDACTED]"`, string(encoded))

	require.Empty(t, fmt.Sprint(interfaces.Secret("")))
}

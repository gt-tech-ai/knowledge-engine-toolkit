package unit_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
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

	var src interfaces.ConnectorSource = mocks.NewMockConnectorSource(ctrl)
	var builder interfaces.SourceBuilder = mocks.NewMockSourceBuilder(ctrl)

	require.NotNil(t, src)
	require.NotNil(t, builder)
}

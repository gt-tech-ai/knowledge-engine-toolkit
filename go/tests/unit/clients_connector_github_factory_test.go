package unit_test

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// ghEnterpriseHost is the GitHub Enterprise Server host the builder test configures,
// so a base URL the builder dropped would show as a request to api.github.com.
const ghEnterpriseHost = "ghe.example"

// TestConnectorSourceBuilder_GitHubKind tests that the connector builder turns a github
// SourceConfig into a working, decorated source.
//
// Why this test is important:
//   - The builder is how a consumer reaches the github lister: the kind must map the
//     SourceConfig's repository, branch and App fields onto the backend, mint the
//     installation token with the App key, and call the configured API root. A wrong
//     mapping would only show up against live GitHub.
//
// What it tests:
//   - Build(kind=github) over a mock HTTP doer and a fixed clock → TestConnection first
//     POSTs the access_tokens exchange with a valid App JWT, then lists main's tree with
//     the installation token and returns its 6 files, every request at the configured
//     (Enterprise) base URL.
func TestConnectorSourceBuilder_GitHubKind(t *testing.T) {
	t.Parallel()

	key, pemKey := ghKey(t)
	files := ghServe(t, ghRoutes())
	tokenPath := "/app/installations/" + strconv.Itoa(ghInstallationID) + "/access_tokens"

	doer := mocks.NewMockHTTPDoer(gomock.NewController(t))
	doer.EXPECT().Do(gomock.Any()).AnyTimes().
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, ghEnterpriseHost, req.URL.Host)

			if req.URL.Path != tokenPath {
				return files.Do(req)
			}

			assert.Equal(t, http.MethodPost, req.Method)
			ghVerifyJWT(t, req, key)

			return ghTokenReply(ghToken, ghClock().Add(ghTokenLife)), nil
		})

	cfg := stdx.Zero[connector.Config]()

	cfg.GitHubBaseURL = "https://" + ghEnterpriseHost

	builder := connector.NewSourceBuilder(cfg, connector.Deps{
		Decorations:   decorate.Decorations{},
		BuildS3Client: nil,
		STS:           nil,
		GitHub:        &connector.GitHubDeps{Doer: doer, Now: ghClock},
	})

	src, err := builder.Build(context.Background(), fixtures.BuildRef(
		func(row *interfaces.SourceConfig) {
			row.Kind = interfaces.ConnectorKindGitHub
			row.Owner = "acme"
			row.Repo = textWidgets
			row.Branch = "main"
			row.GitHubAppID = ghAppID
			row.InstallationID = ghInstallationID
			row.AppPrivateKey = pemKey
		},
	))
	require.NoError(t, err)

	n, err := src.TestConnection(context.Background())
	require.NoError(t, err)
	assert.Equal(t, ghRegularFiles, n)
}

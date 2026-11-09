package unit_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	awssm "github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/bedrock"
	awsbedrockagent "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/bedrockagent"
	awsiam "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/iam"
	awsroute53 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/route53"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/secretsmanager"
	awsquotas "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/servicequotas"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/ses"
	awssts "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/sts"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestClientsAws_StubsAreTheDefault tests every AWS family's default kind.
//
// Why this test is important:
//   - A composition root boots on the stubs with no AWS account; a stub that calls AWS,
//     panics, or invents a secret would make a dev run touch or fake real
//     infrastructure.
//
// What it tests:
//   - Each family's empty kind is its stub: STS reports the stub account; reading a
//     secret, a quota or a Bedrock data source is NOT_FOUND; writing a secret succeeds;
//     SES, Bedrock, IAM and Route 53 return empty outputs; an unknown kind is
//     INVALID_INPUT.
func TestClientsAws_StubsAreTheDefault(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	decs := fixtures.Decorations()

	stsc, err := awssts.NewFromConfig(
		ctx,
		stdx.Zero[awssts.Config](),
		awssts.Deps{Decorations: decs, API: nil, AWS: nil},
	)
	require.NoError(t, err)

	who, err := stsc.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.Equal(t, awssts.StubAccount, aws.ToString(who.Account))

	api, err := secretsmanager.NewFromConfig(ctx, stdx.Zero[secretsmanager.Config](),
		secretsmanager.Deps{Decorations: decs, API: nil, AWS: nil})
	require.NoError(t, err)

	_, err = api.GetSecretValue(
		ctx,
		&awssm.GetSecretValueInput{
			SecretId:     aws.String(textX),
			VersionId:    nil,
			VersionStage: nil,
		},
	)
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	put, err := api.PutSecretValue(
		ctx,
		fixtures.BuildRef(func(row *awssm.PutSecretValueInput) {
			row.SecretId = aws.String(textX)
		}),
	)
	require.NoError(t, err)
	assert.Equal(t, textX, aws.ToString(put.Name))

	currentAPI, err := awsquotas.NewFromConfig(
		ctx,
		stdx.Zero[awsquotas.Config](),
		awsquotas.Deps{Decorations: decs, API: nil, AWS: nil},
	)
	require.NoError(t, err)

	_, err = currentAPI.GetServiceQuota(ctx, new(servicequotas.GetServiceQuotaInput))
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	sesc, err := ses.NewFromConfig(
		ctx,
		stdx.Zero[ses.Config](),
		ses.Deps{Decorations: decs, API: nil, AWS: nil},
	)
	require.NoError(t, err)

	_, err = sesc.GetAccount(ctx, &sesv2.GetAccountInput{})

	testClientsAwsStubsAreTheDefaultBuildsEveryStub(ctx, t, decs, err)
}

// testClientsAwsStubsAreTheDefaultBuildsEveryStub continues
// TestClientsAws_StubsAreTheDefault: builds every stub.
func testClientsAwsStubsAreTheDefaultBuildsEveryStub(
	ctx context.Context, t *testing.T, decs decorate.Decorations, err error,
) {
	t.Helper()

	require.NoError(t, err)

	api, err := bedrock.NewFromConfig(
		ctx,
		stdx.Zero[bedrock.Config](),
		bedrock.Deps{Decorations: decs, API: nil, AWS: nil},
	)
	require.NoError(t, err)

	_, err = api.Converse(ctx, new(bedrockruntime.ConverseInput))
	require.NoError(t, err)

	agent, err := awsbedrockagent.NewFromConfig(
		ctx,
		stdx.Zero[awsbedrockagent.Config](),
		awsbedrockagent.Deps{Decorations: decs, API: nil, AWS: nil},
	)
	require.NoError(t, err)

	_, err = agent.GetDataSource(ctx, new(bedrockagent.GetDataSourceInput))
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	iamc, err := awsiam.NewFromConfig(
		ctx,
		stdx.Zero[awsiam.Config](),
		awsiam.Deps{Decorations: decs, API: nil, AWS: nil},
	)
	require.NoError(t, err)

	_, err = iamc.SimulatePrincipalPolicy(ctx, new(iam.SimulatePrincipalPolicyInput))
	require.NoError(t, err)

	r53, err := awsroute53.NewFromConfig(
		ctx,
		stdx.Zero[awsroute53.Config](),
		awsroute53.Deps{Decorations: decs, API: nil, AWS: nil},
	)
	require.NoError(t, err)

	_, err = r53.ListHostedZonesByName(ctx, new(route53.ListHostedZonesByNameInput))
	require.NoError(t, err)

	_, err = ses.NewFromConfig(
		ctx,
		ses.Config{Kind: "smtp", Name: ""},
		ses.Deps{Decorations: decs, API: nil, AWS: nil},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestClientsAws_SDKKindRunsThroughTheTier tests the sdk kind over an injected client.
//
// Why this test is important:
//   - Every AWS call must be observable and tunable like any client's, and a write
//     whose replay repeats its effect (a new secret version, a billed model call) must
//     never be retried.
//
// What it tests:
//   - The sdk kind uses an injected API, returns its output unchanged and counts the
//     call as its snake_case operation (a secret describe, a Bedrock data-source read);
//     a transient failure of PutSecretValue reaches the caller after one call.
func TestClientsAws_SDKKindRunsThroughTheTier(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	reg := promclient.NewRegistry()
	decs := decorate.Fixed(fixtures.BuildRef(func(row *decorate.Deps) {
		row.Metrics = prom.NewFromRegistry(reg)
	}))
	api := mocks.NewMockAWSSecretsManagerAPI(gomock.NewController(t))
	currentAPI, err := secretsmanager.NewFromConfig(ctx,
		secretsmanager.Config{Kind: secretsmanager.KindSDK, Name: ""},
		secretsmanager.Deps{Decorations: decs, API: api, AWS: nil})
	require.NoError(t, err)

	api.EXPECT().DescribeSecret(gomock.Any(), gomock.Any()).
		Return(fixtures.BuildRef(func(row *awssm.DescribeSecretOutput) {
			row.Name = aws.String(textS)
		}), nil)

	out, err := currentAPI.DescribeSecret(
		ctx,
		&awssm.DescribeSecretInput{SecretId: aws.String(textS)},
	)
	require.NoError(t, err)
	assert.Equal(t, textS, aws.ToString(out.Name))
	assert.InDelta(
		t,
		wantClientOpCounter,
		clientOpCounter(t, reg, "describe_secret"),
		0,
	)

	agentAPI := mocks.NewMockAWSBedrockAgentAPI(gomock.NewController(t))
	agent, err := awsbedrockagent.NewFromConfig(ctx,
		awsbedrockagent.Config{Kind: awsbedrockagent.KindSDK, Name: ""},
		awsbedrockagent.Deps{Decorations: decs, API: agentAPI, AWS: nil})
	require.NoError(t, err)
	agentAPI.EXPECT().GetDataSource(gomock.Any(), gomock.Any()).
		Return(new(bedrockagent.GetDataSourceOutput), nil)

	_, err = agent.GetDataSource(ctx, new(bedrockagent.GetDataSourceInput))
	require.NoError(t, err)
	assert.InDelta(
		t,
		wantClientOpCounter,
		clientOpCounter(t, reg, "get_data_source"),
		0,
	)

	api.EXPECT().PutSecretValue(gomock.Any(), gomock.Any()).
		Return(nil, apperr.New(apperr.CodeUnavailable, "throttled")).Times(1)

	_, err = currentAPI.PutSecretValue(
		ctx,
		fixtures.BuildRef(func(row *awssm.PutSecretValueInput) {
			row.SecretId = aws.String(textS)
		}),
	)
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
}

package unit_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
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
	awsiam "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/iam"
	awsroute53 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/route53"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/secretsmanager"
	awsquotas "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/servicequotas"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/ses"
	awssts "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/sts"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
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
//     secret or a quota is NOT_FOUND; writing a secret succeeds; SES, Bedrock, IAM and
//     Route 53 return empty outputs; an unknown kind is INVALID_INPUT.
func TestClientsAws_StubsAreTheDefault(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	decs := fixtures.Decorations()

	stsc, err := awssts.NewFromConfig(
		ctx,
		awssts.Config{},
		awssts.Deps{Decorations: decs},
	)
	require.NoError(t, err)
	who, err := stsc.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.Equal(t, awssts.StubAccount, aws.ToString(who.Account))

	sm, err := secretsmanager.NewFromConfig(ctx, secretsmanager.Config{},
		secretsmanager.Deps{Decorations: decs})
	require.NoError(t, err)
	_, err = sm.GetSecretValue(ctx, &awssm.GetSecretValueInput{SecretId: aws.String("x")})
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
	put, err := sm.PutSecretValue(
		ctx,
		&awssm.PutSecretValueInput{SecretId: aws.String("x")},
	)
	require.NoError(t, err)
	assert.Equal(t, "x", aws.ToString(put.Name))

	sq, err := awsquotas.NewFromConfig(
		ctx,
		awsquotas.Config{},
		awsquotas.Deps{Decorations: decs},
	)
	require.NoError(t, err)
	_, err = sq.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{})
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	sesc, err := ses.NewFromConfig(ctx, ses.Config{}, ses.Deps{Decorations: decs})
	require.NoError(t, err)
	_, err = sesc.GetAccount(ctx, &sesv2.GetAccountInput{})
	require.NoError(t, err)
	br, err := bedrock.NewFromConfig(
		ctx,
		bedrock.Config{},
		bedrock.Deps{Decorations: decs},
	)
	require.NoError(t, err)
	_, err = br.Converse(ctx, &bedrockruntime.ConverseInput{})
	require.NoError(t, err)
	iamc, err := awsiam.NewFromConfig(
		ctx,
		awsiam.Config{},
		awsiam.Deps{Decorations: decs},
	)
	require.NoError(t, err)
	_, err = iamc.SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{})
	require.NoError(t, err)
	r53, err := awsroute53.NewFromConfig(
		ctx,
		awsroute53.Config{},
		awsroute53.Deps{Decorations: decs},
	)
	require.NoError(t, err)
	_, err = r53.ListHostedZonesByName(ctx, &route53.ListHostedZonesByNameInput{})
	require.NoError(t, err)

	_, err = ses.NewFromConfig(ctx, ses.Config{Kind: "smtp"}, ses.Deps{Decorations: decs})
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
//     call as its snake_case operation; a transient failure of PutSecretValue reaches
//     the caller after one call.
func TestClientsAws_SDKKindRunsThroughTheTier(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	reg := promclient.NewRegistry()
	decs := decorate.Fixed(decorate.Deps{
		Observability: decorate.Observability{Metrics: prom.NewFromRegistry(reg)},
	})
	api := mocks.NewMockAWSSecretsManagerAPI(gomock.NewController(t))
	sm, err := secretsmanager.NewFromConfig(ctx,
		secretsmanager.Config{Kind: secretsmanager.KindSDK},
		secretsmanager.Deps{Decorations: decs, API: api})
	require.NoError(t, err)

	api.EXPECT().DescribeSecret(gomock.Any(), gomock.Any()).
		Return(&awssm.DescribeSecretOutput{Name: aws.String("s")}, nil)
	out, err := sm.DescribeSecret(
		ctx,
		&awssm.DescribeSecretInput{SecretId: aws.String("s")},
	)
	require.NoError(t, err)
	assert.Equal(t, "s", aws.ToString(out.Name))
	assert.InDelta(t, 1.0, clientOpCounter(t, reg, "describe_secret"), 0)

	api.EXPECT().PutSecretValue(gomock.Any(), gomock.Any()).
		Return(nil, apperr.New(apperr.CodeUnavailable, "throttled")).Times(1)
	_, err = sm.PutSecretValue(ctx, &awssm.PutSecretValueInput{SecretId: aws.String("s")})
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
}

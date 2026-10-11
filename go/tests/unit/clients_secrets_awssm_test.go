package unit_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/awssm"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestAWSSMSource_GetExtractsJSONField tests that the Secrets Manager Source fetches a
// secret and extracts the configured JSON field as the credential value.
//
// Why this test is important:
//   - Runtime credentials live in a single JSON secret per class (e.g.
//     app/<env>/e2e/config); reading the wrong field, or failing to parse the JSON,
//     hands the caller the wrong credential.
//
// What it tests:
//   - Get resolves the Ref's secret id, decodes the SecretString JSON, and returns the
//     mapped field's value.
func TestAWSSMSource_GetExtractsJSONField(t *testing.T) {
	ctrl := gomock.NewController(t)
	api := mocks.NewMockSecretsManagerAPI(ctrl)
	blob := `{"client_id":"cid","client_secret":"csecret"}`
	api.EXPECT().
		GetSecretValue(gomock.Any(), gomock.Any()).
		Return(&secretsmanager.GetSecretValueOutput{SecretString: aws.String(blob)}, nil)

	ref := types.Ref{Env: "staging", Class: "e2e", Field: "client_secret"}
	src := awssm.New(api, map[types.Ref]awssm.Location{
		ref: {SecretID: "app/staging/e2e/config", Field: "client_secret"},
	})

	got, err := src.Get(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, "csecret", got.Reveal())
}

// TestAWSSMSource_MissingFieldIsNotFound tests that a secret lacking the mapped field
// yields a coded not-found error.
//
// Why this test is important:
//   - A misconfigured secret must fail explicitly rather than return an empty value the
//     caller injects as if it were real.
//
// What it tests:
//   - Get returns CodeNotFound when the decoded JSON has no such field.
func TestAWSSMSource_MissingFieldIsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	api := mocks.NewMockSecretsManagerAPI(ctrl)
	api.EXPECT().
		GetSecretValue(gomock.Any(), gomock.Any()).
		Return(
			&secretsmanager.GetSecretValueOutput{
				SecretString: aws.String(`{"client_id":"cid"}`),
			},
			nil,
		)

	ref := types.Ref{Env: "staging", Class: "e2e", Field: "client_secret"}
	src := awssm.New(api, map[types.Ref]awssm.Location{
		ref: {SecretID: "app/staging/e2e/config", Field: "client_secret"},
	})

	_, err := src.Get(context.Background(), ref)
	require.True(t, apperr.Is(err, apperr.CodeNotFound))
}

// TestAWSSMSource_SDKErrorIsUpstream tests that an SDK failure is surfaced as a coded
// upstream error.
//
// Why this test is important:
//   - A throttled/unavailable Secrets Manager is a transient upstream failure;
//     classifying it correctly lets resilience decorators retry rather than treating it
//     as fatal.
//
// What it tests:
//   - Get wraps a GetSecretValue error as CodeUpstream.
func TestAWSSMSource_SDKErrorIsUpstream(t *testing.T) {
	ctrl := gomock.NewController(t)
	api := mocks.NewMockSecretsManagerAPI(ctrl)
	api.EXPECT().
		GetSecretValue(gomock.Any(), gomock.Any()).
		Return(nil, apperr.Sentinel("throttled"))

	ref := types.Ref{Env: "staging", Class: "e2e", Field: "client_secret"}
	src := awssm.New(api, map[types.Ref]awssm.Location{
		ref: {SecretID: "app/staging/e2e/config", Field: "client_secret"},
	})

	_, err := src.Get(context.Background(), ref)
	require.True(t, apperr.Is(err, apperr.CodeUpstream))
}

// TestAWSSMSource_PutIsUnsupported tests that writing to the Secrets Manager Source fails
// loudly.
//
// Why this test is important:
//   - Secrets Manager secrets are written by IaC out-of-band; a silent no-op Put would
//     lose a written credential and mask a routing mistake.
//
// What it tests:
//   - Put returns CodeInvalidInput (unsupported) without calling the SDK.
func TestAWSSMSource_PutIsUnsupported(t *testing.T) {
	ctrl := gomock.NewController(t)
	api := mocks.NewMockSecretsManagerAPI(ctrl) // no SDK call expected.

	ref := types.Ref{Env: "staging", Class: "e2e", Field: "client_secret"}
	src := awssm.New(api, map[types.Ref]awssm.Location{
		ref: {SecretID: "app/staging/e2e/config", Field: "client_secret"},
	})

	err := src.Put(context.Background(), ref, types.NewSecret("v"))
	require.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}

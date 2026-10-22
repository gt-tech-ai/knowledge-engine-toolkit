package unit_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	sqtypes "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/probe/readiness"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestOIDCProbe tests that the oidc probe passes on a 200 OIDC-discovery
// response and fails on any other status.
//
// Why this test is important:
//   - a mistyped or un-provisioned tenant domain must be caught up front; the probe's
//     health signal is the discovery document, so a non-200 has to be a FAIL, not a
//     silent pass.
//
// What it tests:
//   - 200 → StatusPass; 404 → StatusFail naming the status.
func TestOIDCProbe(t *testing.T) {
	t.Parallel()

	newResp := func(code int) *http.Response {
		return &http.Response{
			StatusCode: code,
			Status:     http.StatusText(code),
			Body:       io.NopCloser(strings.NewReader("{}")),
		}
	}

	t.Run("pass_on_200", func(t *testing.T) {
		t.Parallel()
		doer := mocks.NewMockHTTPDoer(gomock.NewController(t))
		//nolint:bodyclose // a mock response: the code under test closes the body
		doer.EXPECT().Do(gomock.Any()).Return(newResp(http.StatusOK), nil)

		got := readiness.OIDC{HTTP: doer, Domain: "acme.us.auth0.com"}.
			Run(t.Context(), readiness.Target{KubeContext: "staging"})

		assert.Equal(t, types.StatusPass, got.Status)
	})

	// A 404 from discovery fails, and the GET targets the tenant's discovery URL.
	t.Run("fails_when_discovery_not_200", func(t *testing.T) {
		t.Parallel()
		doer := mocks.NewMockHTTPDoer(gomock.NewController(t))
		var gotURL string
		doer.EXPECT().
			Do(gomock.Any()).
			DoAndReturn(func(r *http.Request) (*http.Response, error) {
				gotURL = r.URL.String()
				return newResp(http.StatusNotFound), nil
			})

		got := readiness.OIDC{HTTP: doer, Domain: "acme.us.auth0.com"}.
			Run(t.Context(), readiness.Target{KubeContext: "staging"})

		assert.Equal(t, types.StatusFail, got.Status)
		assert.Equal(
			t,
			"https://acme.us.auth0.com/.well-known/openid-configuration",
			gotURL,
		)
	})
}

// TestSecretsProbe tests that the secrets probe fails when any required secret is absent,
// skips when none are configured, and passes when all are present.
//
// Why this test is important:
//   - a missing bootstrap secret (auth0-bootstrap, argocd-repo) breaks onboarding at
//     apply time; the merged probe must name every missing secret, not just abort on the
//     first.
//
// What it tests:
//   - a DescribeSecret error for one name → StatusFail naming it;
//   - empty Names → StatusSkip; all present → StatusPass.
func TestSecretsProbe(t *testing.T) {
	t.Parallel()

	t.Run("skips_when_no_names", func(t *testing.T) {
		t.Parallel()
		sm := mocks.NewMockAWSSecretsManagerAPI(
			gomock.NewController(t),
		) // no calls expected
		got := readiness.Secrets{
			SM: sm,
		}.Run(
			t.Context(),
			readiness.Target{KubeContext: "staging"},
		)
		assert.Equal(t, types.StatusSkip, got.Status)
	})

	// test_secrets_probe_fails_when_a_secret_is_missing
	t.Run("fails_when_a_secret_is_missing", func(t *testing.T) {
		t.Parallel()
		sm := mocks.NewMockAWSSecretsManagerAPI(gomock.NewController(t))
		sm.EXPECT().DescribeSecret(gomock.Any(), gomock.Any()).
			DoAndReturn(func(
				_ context.Context,
				in *secretsmanager.DescribeSecretInput,
				_ ...func(*secretsmanager.Options),
			) (*secretsmanager.DescribeSecretOutput, error) {
				if aws.ToString(in.SecretId) == "argocd-repo" {
					return nil, apperr.New(
						apperr.CodeInternal,
						"ResourceNotFoundException",
					)
				}
				return &secretsmanager.DescribeSecretOutput{}, nil
			}).
			Times(2)

		got := readiness.Secrets{
			SM:    sm,
			Names: []string{"auth0-bootstrap", "argocd-repo"},
		}.
			Run(
				t.Context(),
				readiness.Target{KubeContext: "staging"},
			)

		assert.Equal(t, types.StatusFail, got.Status)
		assert.Contains(t, got.Detail, "argocd-repo")
		assert.NotContains(t, got.Detail, "auth0-bootstrap present")
	})

	t.Run("pass_when_all_present", func(t *testing.T) {
		t.Parallel()
		sm := mocks.NewMockAWSSecretsManagerAPI(gomock.NewController(t))
		sm.EXPECT().DescribeSecret(gomock.Any(), gomock.Any()).
			Return(&secretsmanager.DescribeSecretOutput{}, nil).Times(2)

		got := readiness.Secrets{
			SM:    sm,
			Names: []string{"auth0-bootstrap", "argocd-repo"},
		}.
			Run(
				t.Context(),
				readiness.Target{KubeContext: "staging"},
			)

		assert.Equal(t, types.StatusPass, got.Status)
	})
}

// TestDNSProbe tests that the dns probe fails when the apex has a hosted zone but no
// public NS delegation, and passes when both a zone exists and delegation resolves.
//
// Why this test is important:
//   - a hosted zone created but not delegated (NS not yet cut over) is the classic
//     half-ready DNS state; the probe must catch it rather than pass on the zone's mere
//     existence.
//
// What it tests:
//   - zone present + empty LookupNS → StatusFail (delegation absent);
//   - zone present + non-empty LookupNS → StatusPass.
func TestDNSProbe(t *testing.T) {
	t.Parallel()

	zoneListing := &route53.ListHostedZonesByNameOutput{
		HostedZones: []r53types.HostedZone{{Name: aws.String("acme.com.")}},
	}

	// test_dns_probe_fails_when_ns_delegation_absent
	t.Run("fails_when_ns_delegation_absent", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		r53 := mocks.NewMockAWSRoute53API(ctrl)
		r53.EXPECT().
			ListHostedZonesByName(gomock.Any(), gomock.Any()).
			Return(zoneListing, nil)
		res := mocks.NewMockNSResolver(ctrl)
		res.EXPECT().
			LookupNS(gomock.Any(), "acme.com").
			Return(nil, apperr.New(apperr.CodeInternal, "NXDOMAIN"))

		got := readiness.DNS{Resolver: res, Route53: r53, ApexDomain: "acme.com"}.
			Run(t.Context(), readiness.Target{KubeContext: "staging"})

		assert.Equal(t, types.StatusFail, got.Status)
		assert.Contains(t, got.Detail, "delegation")
	})

	t.Run("pass_when_zone_and_delegation", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		r53 := mocks.NewMockAWSRoute53API(ctrl)
		r53.EXPECT().
			ListHostedZonesByName(gomock.Any(), gomock.Any()).
			Return(zoneListing, nil)
		res := mocks.NewMockNSResolver(ctrl)
		res.EXPECT().LookupNS(gomock.Any(), "acme.com").
			Return([]*net.NS{{Host: "ns-1.awsdns-00.com."}}, nil)

		got := readiness.DNS{Resolver: res, Route53: r53, ApexDomain: "acme.com"}.
			Run(t.Context(), readiness.Target{KubeContext: "staging"})

		assert.Equal(t, types.StatusPass, got.Status)
	})
}

// TestSESProbe tests that the ses probe fails when the account is sandboxed and passes
// when production access is enabled.
//
// Why this test is important:
//   - a sandboxed SES account can only mail verified identities, so the notification
//     pipeline silently drops mail to real users; the probe must surface the sandbox as a
//     FAIL.
//
// What it tests:
//   - ProductionAccessEnabled=false → StatusFail; true → StatusPass.
func TestSESProbe(t *testing.T) {
	t.Parallel()

	// test_ses_probe_fails_when_production_access_disabled
	t.Run("fails_when_production_access_disabled", func(t *testing.T) {
		t.Parallel()
		ses := mocks.NewMockAWSSESAPI(gomock.NewController(t))
		ses.EXPECT().GetAccount(gomock.Any(), gomock.Any()).
			Return(&sesv2.GetAccountOutput{ProductionAccessEnabled: false}, nil)

		got := readiness.SES{
			SES: ses,
		}.Run(
			t.Context(),
			readiness.Target{KubeContext: "staging"},
		)

		assert.Equal(t, types.StatusFail, got.Status)
		assert.Contains(t, got.Detail, "sandbox")
	})

	t.Run("pass_when_production_access_enabled", func(t *testing.T) {
		t.Parallel()
		ses := mocks.NewMockAWSSESAPI(gomock.NewController(t))
		ses.EXPECT().GetAccount(gomock.Any(), gomock.Any()).
			Return(&sesv2.GetAccountOutput{ProductionAccessEnabled: true}, nil)

		got := readiness.SES{
			SES: ses,
		}.Run(
			t.Context(),
			readiness.Target{KubeContext: "staging"},
		)

		assert.Equal(t, types.StatusPass, got.Status)
	})
}

// TestBedrockProbe tests that the bedrock probe suppresses its billed call under
// --no-invoke and, otherwise, invokes each DISTINCT configured model id exactly once with
// a one-token cap.
//
// Why this test is important:
//   - bedrock is the only probe that spends money; --no-invoke must guarantee zero calls
//     (UNVERIFIED), and a normal run must be config-driven and de-duplicated (two config
//     keys naming one model cost one call), each request minimal (maxTokens=1) — proving
//     the probe reads the ids from config rather than hardcoding a fixed call set.
//
// What it tests:
//   - NoInvoke=true → StatusUnverified and zero Converse calls;
//   - three ids with one duplicate → exactly two Converse calls, each MaxTokens=1, one
//     per distinct id.
func TestBedrockProbe(t *testing.T) {
	t.Parallel()

	// test_bedrock_probe_unverified_with_no_invoke
	t.Run("unverified_with_no_invoke", func(t *testing.T) {
		t.Parallel()
		br := mocks.NewMockAWSBedrockAPI(
			gomock.NewController(t),
		) // no Converse call expected

		got := readiness.Bedrock{
			Bedrock:  br,
			ModelIDs: []string{"sonnet", "haiku"},
		}.
			Run(
				t.Context(),
				readiness.Target{NoInvoke: true},
			)

		assert.Equal(t, types.StatusWarn, got.Status)
		detail, unverified := readiness.IsUnverified(got)
		assert.True(t, unverified, "a skipped billed call is reported unverified")
		assert.True(t, strings.HasPrefix(detail, "--no-invoke:"), detail)
	})

	// test_bedrock_probe_invokes_each_configured_model_once_with_one_token
	t.Run("invokes_each_configured_model_once_with_one_token", func(t *testing.T) {
		t.Parallel()
		br := mocks.NewMockAWSBedrockAPI(gomock.NewController(t))
		var calledIDs []string
		br.EXPECT().Converse(gomock.Any(), gomock.Any()).
			DoAndReturn(func(
				_ context.Context,
				in *bedrockruntime.ConverseInput,
				_ ...func(*bedrockruntime.Options),
			) (*bedrockruntime.ConverseOutput, error) {
				calledIDs = append(calledIDs, aws.ToString(in.ModelId))
				require.NotNil(t, in.InferenceConfig)
				require.NotNil(t, in.InferenceConfig.MaxTokens)
				assert.Equal(t, int32(1), *in.InferenceConfig.MaxTokens)
				return &bedrockruntime.ConverseOutput{}, nil
			}).
			Times(2)

		// sonnet, haiku, haiku (dup) → 2 distinct ids → 2 calls.
		got := readiness.Bedrock{
			Bedrock:  br,
			ModelIDs: []string{"sonnet", "haiku", "haiku"},
		}.
			Run(
				t.Context(),
				readiness.Target{KubeContext: "staging"},
			)

		assert.Equal(t, types.StatusPass, got.Status)
		assert.ElementsMatch(t, []string{"sonnet", "haiku"}, calledIDs)
	})
}

// TestQuotasProbe tests that the quotas probe skips when no minimums are configured and
// fails when an applied quota value is below its configured minimum.
//
// Why this test is important:
//   - the quota minimums seed EMPTY (no config source yet), so the probe must SKIP rather
//     than make pointless calls or falsely PASS; once populated, a shortfall must be a
//     FAIL.
//
// What it tests:
//   - empty Minimums → StatusSkip and zero GetServiceQuota calls;
//   - applied value below the minimum → StatusFail naming the shortfall.
func TestQuotasProbe(t *testing.T) {
	t.Parallel()

	// test_quotas_probe_skips_when_minimums_unset
	t.Run("skips_when_minimums_unset", func(t *testing.T) {
		t.Parallel()
		q := mocks.NewMockAWSServiceQuotasAPI(
			gomock.NewController(t),
		) // no calls expected
		got := readiness.Quotas{
			Quotas: q,
		}.Run(
			t.Context(),
			readiness.Target{KubeContext: "staging"},
		)
		assert.Equal(t, types.StatusSkip, got.Status)
	})

	t.Run("fails_when_below_minimum", func(t *testing.T) {
		t.Parallel()
		q := mocks.NewMockAWSServiceQuotasAPI(gomock.NewController(t))
		q.EXPECT().GetServiceQuota(gomock.Any(), gomock.Any()).
			Return(
				&servicequotas.GetServiceQuotaOutput{
					Quota: &sqtypes.ServiceQuota{Value: aws.Float64(2)},
				},
				nil,
			)

		got := readiness.Quotas{
			Quotas:   q,
			Minimums: map[string]int{"bedrock/L-1234": 10},
		}.
			Run(
				t.Context(),
				readiness.Target{KubeContext: "staging"},
			)

		assert.Equal(t, types.StatusFail, got.Status)
		assert.Contains(t, got.Detail, "bedrock/L-1234")
	})
}

// TestRegistryProbe tests that the registry-pull probe fails when an ECR pull action is
// denied and passes when every action is allowed.
//
// Why this test is important:
//   - a missing ECR pull grant surfaces only as a runtime ImagePullBackOff; simulating
//     the policy up front turns that into an actionable pre-deploy FAIL.
//
// What it tests:
//   - an explicitDeny evaluation → StatusFail naming the denied action;
//   - all allowed → StatusPass.
func TestRegistryProbe(t *testing.T) {
	t.Parallel()

	// test_registry_probe_fails_when_pull_denied
	t.Run("fails_when_pull_denied", func(t *testing.T) {
		t.Parallel()
		im := mocks.NewMockAWSIAMAPI(gomock.NewController(t))
		im.EXPECT().SimulatePrincipalPolicy(gomock.Any(), gomock.Any()).
			Return(&iam.SimulatePrincipalPolicyOutput{
				EvaluationResults: []iamtypes.EvaluationResult{
					{
						EvalActionName: aws.String("ecr:GetAuthorizationToken"),
						EvalDecision:   iamtypes.PolicyEvaluationDecisionTypeAllowed,
					},
					{
						EvalActionName: aws.String("ecr:BatchGetImage"),
						EvalDecision:   iamtypes.PolicyEvaluationDecisionTypeExplicitDeny,
					},
				},
			}, nil)

		got := readiness.Registry{
			IAM:     im,
			RoleARN: "arn:aws:iam::111122223333:role/deploy",
		}.
			Run(
				t.Context(),
				readiness.Target{KubeContext: "staging"},
			)

		assert.Equal(t, types.StatusFail, got.Status)
		assert.Contains(t, got.Detail, "ecr:BatchGetImage")
	})

	t.Run("pass_when_all_allowed", func(t *testing.T) {
		t.Parallel()
		im := mocks.NewMockAWSIAMAPI(gomock.NewController(t))
		im.EXPECT().SimulatePrincipalPolicy(gomock.Any(), gomock.Any()).
			Return(&iam.SimulatePrincipalPolicyOutput{
				EvaluationResults: []iamtypes.EvaluationResult{
					{
						EvalActionName: aws.String("ecr:GetAuthorizationToken"),
						EvalDecision:   iamtypes.PolicyEvaluationDecisionTypeAllowed,
					},
				},
			}, nil)

		got := readiness.Registry{
			IAM:     im,
			RoleARN: "arn:aws:iam::111122223333:role/deploy",
		}.
			Run(
				t.Context(),
				readiness.Target{KubeContext: "staging"},
			)

		assert.Equal(t, types.StatusPass, got.Status)
	})
}

// TestClusterProbe tests that the cluster-access probe fails when kubectl cannot reach
// the cluster and passes on an authorized "yes".
//
// Why this test is important:
//   - an unreachable or unauthorized cluster context must be a clean FAIL before any
//     deploy step assumes kubectl works; the probe's signal is `auth can-i`'s yes/no
//     answer.
//
// What it tests:
//   - RunBuffered "no" (non-zero) → StatusFail; "yes" → StatusPass against the right
//     context.
func TestClusterProbe(t *testing.T) {
	t.Parallel()

	// test_cluster_probe_fails_when_cannot_access
	t.Run("fails_when_cannot_access", func(t *testing.T) {
		t.Parallel()
		runner := mocks.NewMockCommandRunner(gomock.NewController(t))
		runner.EXPECT().
			RunBuffered(gomock.Any(), gomock.Any(), "kubectl", gomock.Any()).
			Return(
				interfaces.CmdResult{
					Err:      apperr.New(apperr.CodeInternal, "forbidden"),
					Stdout:   []byte("no\n"),
					ExitCode: 1,
				},
			)

		got := readiness.Cluster{Runner: runner}.
			Run(t.Context(), readiness.Target{KubeContext: "staging"})

		assert.Equal(t, types.StatusFail, got.Status)
	})

	t.Run("pass_when_authorized", func(t *testing.T) {
		t.Parallel()
		runner := mocks.NewMockCommandRunner(gomock.NewController(t))
		var gotArgs []string
		runner.EXPECT().
			RunBuffered(gomock.Any(), gomock.Any(), "kubectl", gomock.Any()).
			DoAndReturn(func(_ context.Context, _, _ string, args ...string) interfaces.CmdResult {
				gotArgs = args
				return interfaces.CmdResult{Stdout: []byte("yes\n")}
			})

		got := readiness.Cluster{Runner: runner}.
			Run(t.Context(), readiness.Target{KubeContext: "staging"})

		assert.Equal(t, types.StatusPass, got.Status)
		assert.Equal(
			t,
			[]string{"--context", "staging", "auth", "can-i", "get", "pods"},
			gotArgs,
		)
	})
}

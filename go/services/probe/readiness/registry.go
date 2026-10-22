package readiness

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	iamclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/iam"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ecrPullActions are the ECR actions a workload must be allowed to pull images.
func ecrPullActions() []string {
	return []string{
		"ecr:GetAuthorizationToken",
		"ecr:BatchCheckLayerAvailability",
		"ecr:GetDownloadUrlForLayer",
		"ecr:BatchGetImage",
	}
}

// Registry checks that a role may pull container images from ECR, by simulating the
// pull actions against it, so a missing grant surfaces before a pod's ImagePullBackOff.
type Registry struct {
	// IAM runs the policy simulation.
	IAM iamclient.API

	// RoleARN is the principal simulated.
	RoleARN string
}

// Compile-time assertion that Registry is a readiness probe.
var _ interfaces.Probe[Target] = Registry{}

// Name is "registry-pull".
func (Registry) Name() string { return "registry-pull" }

// Run passes when every pull action is allowed; the failure names each denied one.
func (p Registry) Run(ctx context.Context, _ Target) types.Verdict {
	out, err := p.IAM.SimulatePrincipalPolicy(ctx, &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(p.RoleARN),
		ActionNames:     ecrPullActions(),
	})
	if err != nil {
		return fail(p.Name(), "IAM SimulatePrincipalPolicy failed: "+err.Error())
	}
	var denied []string
	for i := range out.EvaluationResults {
		r := &out.EvaluationResults[i]
		if r.EvalDecision != iamtypes.PolicyEvaluationDecisionTypeAllowed {
			denied = append(denied, aws.ToString(r.EvalActionName))
		}
	}
	if len(denied) > 0 {
		return fail(p.Name(), "role "+p.RoleARN+" is denied ECR pull action(s): "+
			strings.Join(denied, ", "), denied...)
	}
	return pass("role " + p.RoleARN + " is allowed all ECR pull actions")
}

package readiness

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	sesclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/ses"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// SES checks that the account's SES is out of the sandbox, so mail reaches any
// recipient rather than only verified identities.
type SES struct {
	// SES is the SESv2 client.
	SES sesclient.API
}

// Compile-time assertion that SES is a readiness probe.
var _ interfaces.Probe[Target] = SES{}

// Name is "ses".
func (SES) Name() string { return "ses" }

// Run reads the account: production access passes; a sandboxed account fails.
func (p SES) Run(ctx context.Context, _ Target) types.Verdict {
	out, err := p.SES.GetAccount(ctx, &sesv2.GetAccountInput{})
	if err != nil {
		return fail(p.Name(), "SES GetAccount failed: "+err.Error())
	}
	if !out.ProductionAccessEnabled {
		return fail(p.Name(), "SES is in the sandbox (production access disabled)")
	}
	return pass("SES production access enabled")
}

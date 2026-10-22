package readiness

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	smclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/secretsmanager"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Secrets checks that every bootstrap secret a deployment needs exists in Secrets
// Manager; adding a required secret is a Names entry, not a new probe.
type Secrets struct {
	// SM is the Secrets Manager client.
	SM smclient.API

	// Names are the secret ids that must exist.
	Names []string
}

// Compile-time assertion that Secrets is a readiness probe.
var _ interfaces.Probe[Target] = Secrets{}

// Name is "secrets".
func (Secrets) Name() string { return "secrets" }

// Run describes each secret; with no names it skips. Every name is checked, so the
// failure names every missing secret rather than the first.
func (p Secrets) Run(ctx context.Context, _ Target) types.Verdict {
	if len(p.Names) == 0 {
		return skip("no bootstrap secrets configured to check")
	}
	var missing []string
	for _, name := range p.Names {
		if _, err := p.SM.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
			SecretId: aws.String(name),
		}); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fail(
			p.Name(),
			"missing secret(s): "+strings.Join(missing, ", "),
			missing...)
	}
	return pass("all bootstrap secrets present: " + strings.Join(p.Names, ", "))
}

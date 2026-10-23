package readiness

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"

	r53client "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/route53"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// DNS checks that an apex domain has a Route 53 hosted zone and that public DNS
// delegates it (a public resolver sees name servers); either alone is not ready.
type DNS struct {
	// Resolver is the public NS lookup.
	Resolver interfaces.NSResolver

	// Route53 lists the hosted zones.
	Route53 r53client.API

	// ApexDomain is the apex (e.g. "acme.com").
	ApexDomain string
}

// Compile-time assertion that DNS is a readiness probe.
var _ interfaces.Probe[Target] = DNS{}

// Name is "dns".
func (DNS) Name() string { return "dns" }

// Run passes when both legs hold; the failure names whichever is missing.
func (p DNS) Run(ctx context.Context, _ Target) types.Verdict {
	zoneFound, err := p.hostedZoneExists(ctx)
	if err != nil {
		return fail(p.Name(), "list Route 53 hosted zones: "+err.Error())
	}
	ns, nsErr := p.Resolver.LookupNS(ctx, p.ApexDomain)
	delegated := nsErr == nil && len(ns) > 0
	apex := "apex " + p.ApexDomain
	switch {
	case zoneFound && delegated:
		return pass(apex + " has a hosted zone and resolves NS delegation")
	case !zoneFound && !delegated:
		return fail(
			p.Name(),
			apex+" has no Route 53 hosted zone and no public NS delegation",
		)
	case !zoneFound:
		return fail(p.Name(), apex+" resolves NS but has no Route 53 hosted zone")
	default:
		return fail(p.Name(), apex+" has a hosted zone but no public NS delegation yet")
	}
}

// hostedZoneExists reports whether a hosted zone named the apex's fully qualified
// (trailing-dot) form exists.
func (p DNS) hostedZoneExists(ctx context.Context) (bool, error) {
	want := strings.TrimSuffix(p.ApexDomain, ".") + "."
	out, err := p.Route53.ListHostedZonesByName(ctx, &route53.ListHostedZonesByNameInput{
		DNSName: aws.String(want),
	})
	if err != nil {
		return false, err
	}
	for i := range out.HostedZones {
		if aws.ToString(out.HostedZones[i].Name) == want {
			return true, nil
		}
	}
	return false, nil
}

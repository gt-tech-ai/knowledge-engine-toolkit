package readiness

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/servicequotas"

	quotasclient "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/aws/servicequotas"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Quotas checks that each required service quota meets its minimum.
type Quotas struct {
	// Quotas is the Service Quotas client.
	Quotas quotasclient.API

	// Minimums maps "<serviceCode>/<quotaCode>" to the minimum applied value.
	Minimums map[string]int
}

// Compile-time assertion that Quotas is a readiness probe.
var _ interfaces.Probe[Target] = Quotas{}

// Name is "quotas".
func (Quotas) Name() string { return "quotas" }

// Run checks each quota in key order; with no minimums it skips. Every shortfall (a
// malformed key, an unreadable quota, a value below its minimum) is reported.
func (p Quotas) Run(ctx context.Context, _ Target) types.Verdict {
	if len(p.Minimums) == 0 {
		return skip("no quota minimums configured to enforce")
	}
	var shortfalls []string
	keys := make([]string, 0, len(p.Minimums))
	for k := range p.Minimums {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if s := p.shortfall(ctx, key, p.Minimums[key]); s != "" {
			shortfalls = append(shortfalls, s)
		}
	}
	if len(shortfalls) > 0 {
		return fail(p.Name(), "quota shortfall(s): "+strings.Join(shortfalls, ", "),
			shortfalls...)
	}
	return pass("all " + strconv.Itoa(len(p.Minimums)) +
		" configured quota(s) meet their minimum")
}

// shortfall describes why key misses minimum, or returns "" when it meets it.
func (p Quotas) shortfall(ctx context.Context, key string, minimum int) string {
	serviceCode, quotaCode, found := strings.Cut(key, "/")
	if !found || serviceCode == "" || quotaCode == "" {
		return key + " (malformed quota key, want <serviceCode>/<quotaCode>)"
	}
	out, err := p.Quotas.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
		ServiceCode: &serviceCode,
		QuotaCode:   &quotaCode,
	})
	if err != nil {
		return key + " (unreadable: " + err.Error() + ")"
	}
	if out.Quota == nil || out.Quota.Value == nil {
		return key + " (no applied value returned)"
	}
	if got := int(*out.Quota.Value); got < minimum {
		return key + " (" + strconv.Itoa(got) + " < " + strconv.Itoa(minimum) + ")"
	}
	return ""
}

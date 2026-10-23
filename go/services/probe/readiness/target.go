// Package readiness holds the read-only probes that a cloud deployment's prerequisites
// are in place before it is rolled out: bootstrap secrets, DNS delegation, SES production
// access, invokable Bedrock models, service quotas, registry pull grants, an OIDC
// tenant's discovery document and cluster access. Each is an interfaces.Probe[Target]
// over the injected client it reads (a clients/aws API, an HTTPDoer, a CommandRunner),
// so a probe is unit-testable on generated mocks and mutates nothing.
//
// A probe passes, fails with the Detail naming every missing prerequisite (and a Finding
// per item where there are several), skips when nothing is configured to check, or warns
// with a Detail starting Unverified when it declined to make a billed call.
package readiness

import (
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Unverified prefixes the Detail of a warning verdict whose check was not made (a
// billed call skipped under Target.NoInvoke).
const Unverified = "unverified: "

// Target is what one readiness run checks against.
type Target struct {
	// KubeContext is the kubectl context the cluster probe checks.
	KubeContext string

	// NoInvoke skips every billed call; a probe that would make one warns Unverified.
	NoInvoke bool
}

// IsUnverified reports whether v is a warning whose check was not made, and returns its
// Detail without the Unverified prefix.
func IsUnverified(v types.Verdict) (string, bool) {
	if v.Status != types.StatusWarn || !strings.HasPrefix(v.Detail, Unverified) {
		return v.Detail, false
	}
	return strings.TrimPrefix(v.Detail, Unverified), true
}

// pass, fail and skip build the verdicts every probe returns.
func pass(detail string) types.Verdict {
	return types.Verdict{Status: types.StatusPass, Detail: detail}
}

// fail is a failing verdict with one Finding per item, each reported under check.
func fail(check, detail string, items ...string) types.Verdict {
	v := types.Verdict{Status: types.StatusFail, Detail: detail}
	for _, it := range items {
		v.Findings = append(v.Findings, types.Finding{Check: check, Message: it})
	}
	return v
}

// skip is a skipped verdict: nothing was configured to check.
func skip(detail string) types.Verdict {
	return types.Verdict{Status: types.StatusSkip, Detail: detail}
}

package readiness

import (
	"context"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Cluster checks that the operator's kubeconfig reaches the target's cluster, by
// asking `kubectl --context <ctx> auth can-i get pods`.
type Cluster struct {
	// Runner runs kubectl.
	Runner interfaces.CommandRunner
}

// Compile-time assertion that Cluster is a readiness probe.
var _ interfaces.Probe[Target] = Cluster{}

// Name is "cluster-access".
func (Cluster) Name() string { return "cluster-access" }

// Run passes on a "yes"; a non-zero exit, a transport error or a "no" fails.
func (p Cluster) Run(ctx context.Context, t Target) types.Verdict {
	res := p.Runner.RunBuffered(ctx, "", "kubectl",
		"--context", t.KubeContext, "auth", "can-i", "get", "pods")
	answer := strings.TrimSpace(string(res.Stdout))
	if res.Err == nil && answer == "yes" {
		return pass(
			"cluster context " + t.KubeContext + " is reachable (auth can-i: yes)",
		)
	}
	detail := "kubectl --context " + t.KubeContext + " auth can-i get pods"
	switch {
	case answer != "" && answer != "yes":
		detail += " returned " + answer
	case res.Err != nil:
		detail += " failed: " + res.Err.Error()
	}
	return fail(p.Name(), detail)
}

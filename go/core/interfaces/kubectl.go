package interfaces

import (
	"context"
	"time"
)

// KubectlClient runs typed kubectl operations against a cluster.
type KubectlClient interface {
	// Get returns the JSON of the named object of kind in ns, or of every object of
	// kind when name is empty.
	Get(ctx context.Context, ns, kind, name string) ([]byte, error)

	// Apply applies the manifest files in ns (the files' own namespaces when ns is "");
	// no files is CodeInvalidInput.
	Apply(ctx context.Context, ns string, files ...string) error

	// Rollout waits up to timeout for the named workload's rollout to finish.
	Rollout(ctx context.Context, ns, kind, name string, timeout time.Duration) error

	// CurrentContext returns the kubeconfig's current context.
	CurrentContext(ctx context.Context) (string, error)

	// AuthCanI reports whether the caller may verb resource. A denial is (false, nil);
	// a cluster that cannot answer is an error, never a denial.
	AuthCanI(ctx context.Context, verb, resource string) (bool, error)
}

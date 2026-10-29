package kubectl

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every KubectlClient operation of base through the client tier as the
// client named name. Apply is idempotent (server-side merge), so every operation may
// retry; Rollout's own --timeout bounds its wait.
func Decorate(
	base interfaces.KubectlClient,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.KubectlClient, error) {
	b, err := client.NewBase[interfaces.KubectlClient](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, Base: b}, nil
}

// decorator runs each KubectlClient operation through the client tier.
type decorator struct {
	// base is the wrapped client.
	base interfaces.KubectlClient

	// Base runs each operation through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies KubectlClient.
var _ interfaces.KubectlClient = (*decorator)(nil)

// Get reads objects.
func (d *decorator) Get(ctx context.Context, ns, kind, name string) ([]byte, error) {
	return d.Exec(ctx, "get", func(c context.Context) ([]byte, error) {
		return d.base.Get(c, ns, kind, name)
	})
}

// Apply applies manifests.
func (d *decorator) Apply(ctx context.Context, ns string, files ...string) error {
	return d.Do(ctx, "apply", func(c context.Context) error {
		return d.base.Apply(c, ns, files...)
	})
}

// Rollout waits for a rollout.
func (d *decorator) Rollout(
	ctx context.Context,
	ns, kind, name string,
	timeout time.Duration,
) error {
	return d.Do(ctx, "rollout", func(c context.Context) error {
		return d.base.Rollout(c, ns, kind, name, timeout)
	})
}

// AuthCanI asks the cluster about one permission.
func (d *decorator) AuthCanI(ctx context.Context, verb, resource string) (bool, error) {
	return d.Exec(ctx, "auth_can_i", func(c context.Context) (bool, error) {
		return d.base.AuthCanI(c, verb, resource)
	})
}

// CurrentContext reads the kubeconfig.
func (d *decorator) CurrentContext(ctx context.Context) (string, error) {
	return d.Exec(ctx, "current_context", d.base.CurrentContext)
}

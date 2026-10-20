// Package exec is the KubectlClient that runs the kubectl binary through a
// CommandRunner.
package exec

import (
	"context"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Client runs kubectl, optionally pinned to one kubeconfig context.
type Client struct {
	// runner runs kubectl.
	runner interfaces.CommandRunner

	// kubeContext is passed as --context when set.
	kubeContext string
}

// Compile-time assertion that Client satisfies KubectlClient.
var _ interfaces.KubectlClient = (*Client)(nil)

// New returns the Client; kubeContext pins every call to that context when set. It
// does no I/O.
func New(runner interfaces.CommandRunner, kubeContext string) *Client {
	return &Client{runner: runner, kubeContext: kubeContext}
}

// kubectl runs kubectl args and returns stdout or a coded error.
func (c *Client) kubectl(
	ctx context.Context,
	what string,
	args ...string,
) ([]byte, error) {
	if c.kubeContext != "" {
		args = append([]string{"--context", c.kubeContext}, args...)
	}
	return client.Output(
		c.runner.RunBuffered(ctx, "", "kubectl", args...),
		"kubectl "+what,
	)
}

// namespaced prefixes args with -n ns when ns is set.
func namespaced(ns string, args ...string) []string {
	if ns == "" {
		return args
	}
	return append([]string{"-n", ns}, args...)
}

// Get runs `kubectl get kind [name] -o json`.
func (c *Client) Get(ctx context.Context, ns, kind, name string) ([]byte, error) {
	args := []string{"get", kind}
	if name != "" {
		args = append(args, name)
	}
	return c.kubectl(ctx, "get", namespaced(ns, append(args, "-o", "json")...)...)
}

// Apply runs `kubectl apply -f file…`; no files is CodeInvalidInput, since kubectl would
// refuse it.
func (c *Client) Apply(ctx context.Context, ns string, files ...string) error {
	if len(files) == 0 {
		return apperr.New(apperr.CodeInvalidInput, "kubectl apply: no files")
	}
	args := make([]string, 0, 1+2*len(files))
	args = append(args, "apply")
	for _, f := range files {
		args = append(args, "-f", f)
	}
	_, err := c.kubectl(ctx, "apply", namespaced(ns, args...)...)
	return err
}

// Rollout runs `kubectl rollout status kind/name --timeout=…`.
func (c *Client) Rollout(
	ctx context.Context,
	ns, kind, name string,
	timeout time.Duration,
) error {
	_, err := c.kubectl(ctx, "rollout status", namespaced(ns, "rollout", "status",
		kind+"/"+name, "--timeout="+timeout.String())...)
	return err
}

// CurrentContext runs `kubectl config current-context`.
func (c *Client) CurrentContext(ctx context.Context) (string, error) {
	out, err := c.kubectl(ctx, "config current-context", "config", "current-context")
	return strings.TrimSpace(string(out)), err
}

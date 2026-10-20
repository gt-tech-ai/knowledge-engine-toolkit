// Package exec is the HelmClient that runs the helm binary through a CommandRunner.
package exec

import (
	"context"
	"maps"
	"path"
	"slices"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Client runs helm in the codebase root.
type Client struct {
	// runner runs helm.
	runner interfaces.CommandRunner

	// root is the directory helm runs in; spec paths are relative to it.
	root string
}

// Compile-time assertion that Client satisfies HelmClient.
var _ interfaces.HelmClient = (*Client)(nil)

// New returns the Client running helm in root through runner. It does no I/O.
func New(runner interfaces.CommandRunner, root string) *Client {
	return &Client{runner: runner, root: root}
}

// Template runs `helm template <release> <chart> [--namespace] [-f …] [--set …]`.
func (c *Client) Template(ctx context.Context, spec types.RenderSpec) ([]byte, error) {
	release := spec.Release
	if release == "" {
		release = path.Base(spec.ChartDir)
	}
	args := append([]string{"template", release, spec.ChartDir}, values(spec)...)
	return client.Output(
		c.runner.RunBuffered(ctx, c.root, "helm", args...),
		"helm template",
	)
}

// DependencyBuild runs `helm dependency build <chart>`.
func (c *Client) DependencyBuild(ctx context.Context, chartDir string) error {
	_, err := client.Output(
		c.runner.RunBuffered(ctx, c.root, "helm", "dependency", "build", chartDir),
		"helm dependency build")
	return err
}

// Lint runs `helm lint <chart> [--namespace] [-f …] [--set …]`.
func (c *Client) Lint(ctx context.Context, spec types.RenderSpec) error {
	args := append([]string{"lint", spec.ChartDir}, values(spec)...)
	_, err := client.Output(
		c.runner.RunBuffered(ctx, c.root, "helm", args...),
		"helm lint",
	)
	return err
}

// values are spec's namespace, values files and sorted --set flags.
func values(spec types.RenderSpec) []string {
	var args []string
	if spec.Namespace != "" {
		args = append(args, "--namespace", spec.Namespace)
	}
	for _, f := range spec.ValuesFiles {
		args = append(args, "-f", f)
	}
	for _, k := range slices.Sorted(maps.Keys(spec.Set)) {
		args = append(args, "--set", k+"="+spec.Set[k])
	}
	return args
}

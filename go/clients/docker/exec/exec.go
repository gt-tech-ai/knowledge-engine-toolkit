// Package exec is the DockerClient that runs the docker binary through a
// CommandRunner.
package exec

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Client runs docker in a working directory.
type Client struct {
	// runner runs docker.
	runner interfaces.CommandRunner

	// root is the directory docker runs in; spec paths are relative to it.
	root string
}

// Compile-time assertion that Client satisfies DockerClient.
var _ interfaces.DockerClient = (*Client)(nil)

// New returns the Client running docker in root. It does no I/O.
func New(runner interfaces.CommandRunner, root string) *Client {
	return &Client{runner: runner, root: root}
}

// docker runs docker args with env and returns stdout or a coded error.
func (c *Client) docker(
	ctx context.Context,
	what string,
	env []string,
	args ...string,
) ([]byte, error) {
	res := c.runner.RunBufferedWithEnv(ctx, c.root, env, "docker", args...)
	return client.Output(res, "docker "+what)
}

// Build runs `docker buildx build --load` without provenance or SBOM attestations, so
// the image loads into the local daemon.
func (c *Client) Build(ctx context.Context, spec types.BuildSpec) error {
	args := []string{"buildx", "build", "--load", "--provenance=false", "--sbom=false"}
	if spec.Dockerfile != "" {
		args = append(args, "-f", spec.Dockerfile)
	}
	if spec.Target != "" {
		args = append(args, "--target", spec.Target)
	}
	if spec.Platform != "" {
		args = append(args, "--platform", spec.Platform)
	}
	for _, k := range slices.Sorted(maps.Keys(spec.BuildArgs)) {
		args = append(args, "--build-arg", k+"="+spec.BuildArgs[k])
	}
	for _, t := range spec.Tags {
		args = append(args, "-t", t)
	}
	_, err := c.docker(ctx, "build", nil, append(args, spec.Context)...)
	return err
}

// Push runs `docker push ref`.
func (c *Client) Push(ctx context.Context, ref string) error {
	_, err := c.docker(ctx, "push", nil, "push", ref)
	return err
}

// Inspect runs `docker image inspect ref`; a missing image is CodeNotFound.
func (c *Client) Inspect(ctx context.Context, ref string) ([]byte, error) {
	res := c.runner.RunBufferedWithEnv(
		ctx,
		c.root,
		nil,
		"docker",
		"image",
		"inspect",
		ref,
	)
	if res.Err != nil && strings.Contains(string(res.Stderr), "No such image") {
		return nil, apperr.Wrap(res.Err, apperr.CodeNotFound, "no image "+ref)
	}
	return client.Output(res, "docker image inspect")
}

// ComposeUp runs `docker compose … up -d --wait [services]`.
func (c *Client) ComposeUp(ctx context.Context, spec types.ComposeSpec) error {
	args := append(compose(spec), "up", "-d", "--wait")
	_, err := c.docker(ctx, "compose up", spec.Env, append(args, spec.Services...)...)
	return err
}

// ComposeDown runs `docker compose … down [--volumes]`.
func (c *Client) ComposeDown(ctx context.Context, spec types.ComposeSpec) error {
	args := append(compose(spec), "down")
	if spec.RemoveVolumes {
		args = append(args, "--volumes")
	}
	_, err := c.docker(ctx, "compose down", spec.Env, args...)
	return err
}

// compose is the `docker compose` prefix selecting spec's project, files and profiles.
func compose(spec types.ComposeSpec) []string {
	args := []string{"compose"}
	if spec.Project != "" {
		args = append(args, "-p", spec.Project)
	}
	for _, f := range spec.Files {
		args = append(args, "-f", f)
	}
	for _, p := range spec.Profiles {
		args = append(args, "--profile", p)
	}
	return args
}

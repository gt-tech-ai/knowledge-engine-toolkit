// Package exec is the TerraformClient that runs the terraform binary through a
// CommandRunner.
package exec

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Client runs terraform.
type Client struct {
	// runner runs terraform.
	runner interfaces.CommandRunner
}

// Compile-time assertion that Client satisfies TerraformClient.
var _ interfaces.TerraformClient = (*Client)(nil)

// New returns the Client. It does no I/O.
func New(runner interfaces.CommandRunner) *Client { return &Client{runner: runner} }

// terraform runs terraform args in dir.
func (c *Client) terraform(
	ctx context.Context,
	dir string,
	args ...string,
) ([]byte, error) {
	return client.Output(c.runner.RunBuffered(ctx, dir, "terraform", args...),
		"terraform "+args[0])
}

// Init runs `terraform init -input=false`.
func (c *Client) Init(ctx context.Context, dir string) error {
	_, err := c.terraform(ctx, dir, "init", "-input=false")
	return err
}

// Plan runs `terraform plan -input=false -out=out`, then shows the plan.
func (c *Client) Plan(ctx context.Context, dir, out string) (types.Plan, error) {
	if _, err := c.terraform(ctx, dir, "plan", "-input=false", "-out="+out); err != nil {
		return types.Plan{}, err
	}
	return c.Show(ctx, dir, out)
}

// Apply runs `terraform apply -input=false planFile`.
func (c *Client) Apply(ctx context.Context, dir, planFile string) error {
	_, err := c.terraform(ctx, dir, "apply", "-input=false", planFile)
	return err
}

// Show runs `terraform show -json planFile` and parses it.
func (c *Client) Show(ctx context.Context, dir, planFile string) (types.Plan, error) {
	out, err := c.terraform(ctx, dir, "show", "-json", planFile)
	if err != nil {
		return types.Plan{}, err
	}
	return Parse(out)
}

// showJSON is the part of `terraform show -json` a plan is read from.
type showJSON struct {
	// ResourceChanges are the plan's per-resource changes.
	ResourceChanges []struct {
		// Address is the resource address.
		Address string `json:"address"`

		// Type is the resource type.
		Type string `json:"type"`

		// Name is the resource name.
		Name string `json:"name"`

		// Change holds the planned actions.
		Change struct {
			// Actions are the planned actions.
			Actions []string `json:"actions"`
		} `json:"change"`
	} `json:"resource_changes"`
}

// Parse reads `terraform show -json` output into a Plan: no-op and read actions are
// dropped; create adds, update changes, delete destroys, and a replacement (delete and
// create) both adds and destroys.
func Parse(out []byte) (types.Plan, error) {
	var doc showJSON
	if err := json.Unmarshal(out, &doc); err != nil {
		return types.Plan{}, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"parse terraform plan",
		)
	}
	var p types.Plan
	for _, rc := range doc.ResourceChanges {
		acts := rc.Change.Actions
		create, del := slices.Contains(acts, "create"), slices.Contains(acts, "delete")
		update := slices.Contains(acts, "update")
		if !create && !del && !update {
			continue
		}
		p.Changes = append(p.Changes, types.ResourceChange{
			Address: rc.Address, Type: rc.Type, Name: rc.Name, Actions: acts,
		})
		if create {
			p.Add++
		}
		if del {
			p.Destroy++
		}
		if update {
			p.Change++
		}
	}
	return p, nil
}

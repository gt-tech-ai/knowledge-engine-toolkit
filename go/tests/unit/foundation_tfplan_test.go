package unit_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/tfplan"
)

// planChange is one planned resource change in a fixture unit plan: its address,
// Terraform's action list and whether the plan imports it.
type planChange struct {
	address   string
	actions   []string
	importing bool
}

// writeUnitPlan writes what terragrunt's --json-out-dir leaves for one unit: its
// `terraform show -json` rendering carrying changes (and, when outputs is set, a changed
// root output).
func writeUnitPlan(t *testing.T, dir, unit string, outputs bool, changes ...planChange) {
	t.Helper()
	type change struct {
		Importing map[string]string `json:"importing,omitempty"`
		Actions   []string          `json:"actions"`
	}
	type resource struct {
		Address string `json:"address"`
		Change  change `json:"change"`
	}
	doc := struct {
		OutputChanges   map[string]change `json:"output_changes,omitempty"`
		FormatVersion   string            `json:"format_version"`
		ResourceChanges []resource        `json:"resource_changes"`
	}{FormatVersion: "1.2"}
	for _, c := range changes {
		ch := change{Actions: c.actions}
		if c.importing {
			ch.Importing = map[string]string{"id": "adopted"}
		}
		doc.ResourceChanges = append(
			doc.ResourceChanges,
			resource{Address: c.address, Change: ch},
		)
	}
	if outputs {
		doc.OutputChanges = map[string]change{"url": {Actions: []string{"update"}}}
	}
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	unitDir := filepath.Join(dir, unit)
	require.NoError(t, os.MkdirAll(unitDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(unitDir, tfplan.JSONFile), data, 0o600))
}

// TestFoundationTfplan tests the saved-plan reader and its reviewer summary.
//
// Why this test is important:
//   - the summary is what a reviewer approves (a job summary, a PR comment) before the
//     saved plans are applied; a buried destroy, a miscounted replace or a data source
//     read listed as a change would have the review approve something other than what
//     is applied, and a failed plan must still leave a summary naming the failure.
//
// What it tests:
//   - ReadUnits reads every unit under the directory, sorted; a replace counts as one
//     add and one destroy (totals 2 to add, 1 to change, 2 to destroy over four units),
//     an import and a forget are listed, a data source read and a no-op are not, and an
//     output change marks the unit; the Destroys section names the replaced and the
//     deleted resource ahead of the unit table, whose rows run destroying units (eks,
//     iam) first, then the changed vpc, then the unchanged rds; a missing directory
//     reads as no units, a broken JSON is INTERNAL naming the file, an empty plan set
//     renders "No unit has pending changes", and FailedSummary carries the error.
func TestFoundationTfplan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeUnitPlan(
		t,
		dir,
		"vpc",
		true,
		planChange{address: "aws_vpc.main", actions: []string{"update"}},
	)
	writeUnitPlan(
		t,
		dir,
		"rds",
		false,
		planChange{address: "aws_db_instance.db", actions: []string{"no-op"}},
	)
	writeUnitPlan(
		t,
		dir,
		"eks",
		false,
		planChange{address: "aws_eks_cluster.main", actions: []string{"create"}},
		planChange{
			address: "aws_eks_node_group.ng",
			actions: []string{"delete", "create"},
		},
	)
	writeUnitPlan(
		t,
		dir,
		"iam",
		false,
		planChange{address: "aws_iam_role.old", actions: []string{"delete"}},
		planChange{address: "data.aws_iam_policy.p", actions: []string{"read"}},
		planChange{
			address:   "aws_iam_role.adopted",
			actions:   []string{"no-op"},
			importing: true,
		},
		planChange{address: "aws_iam_role.kept", actions: []string{"forget"}},
	)

	plans, err := tfplan.ReadUnits(dir)
	require.NoError(t, err)
	units := make([]string, 0, len(plans))
	for _, p := range plans {
		units = append(units, p.Unit)
	}
	assert.Equal(t, []string{"eks", "iam", "rds", "vpc"}, units)
	assert.True(t, plans[3].HasChanges() && plans[3].Outputs, "vpc changes an output")
	assert.False(t, plans[2].HasChanges(), "rds plans nothing")
	assert.Equal(t, []tfplan.Change{
		{Address: "aws_eks_node_group.ng", Action: tfplan.ActionReplace},
		{Address: "aws_eks_cluster.main", Action: tfplan.ActionCreate},
	}, plans[0].Changes, "destroying changes first")

	got := tfplan.Summary("Terraform plan: `staging`", plans)
	assert.Contains(t, got, "4 unit(s): **2 to add, 1 to change, 2 to destroy.**")
	assert.Contains(t, got, "| eks | `aws_eks_node_group.ng` | replace |")
	assert.Contains(t, got, "| iam | `aws_iam_role.old` | delete |")
	assert.Contains(t, got, "- import `aws_iam_role.adopted`")
	assert.Contains(t, got, "- forget `aws_iam_role.kept`")
	assert.Contains(t, got, "| vpc (outputs change) | 0 | 1 | 0 |")
	assert.NotContains(t, got, "data.aws_iam_policy.p", "a data source read is no change")
	last := strings.Index(got, "#### Destroys")
	require.GreaterOrEqual(t, last, 0, got)
	for _, row := range []string{
		"| eks | 2 | 0 | 1 |", "| iam | 0 | 0 | 1 |", "| vpc (outputs change) | 0 | 1 | 0 |",
		"| rds | 0 | 0 | 0 |",
	} {
		at := strings.Index(got, row)
		require.Greater(
			t,
			at,
			last,
			"destroys, then destroying, changed, unchanged units: %s\n%s",
			row,
			got,
		)
		last = at
	}

	none, err := tfplan.ReadUnits(filepath.Join(dir, "missing"))
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.Contains(t, tfplan.Summary("t", none), "No unit has pending changes.")

	broken := filepath.Join(dir, "broken", tfplan.JSONFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(broken), 0o755))
	require.NoError(t, os.WriteFile(broken, []byte("{"), 0o600))
	_, err = tfplan.ReadUnits(dir)
	require.Equal(t, apperr.CodeInternal, apperr.Code(err))
	assert.Contains(t, err.Error(), broken)

	failed := tfplan.FailedSummary("Terraform plan: `staging`",
		apperr.New(apperr.CodeInternal, "terragrunt plan staging: exit status 1"))
	assert.Contains(t, failed, "### Terraform plan: `staging` (failed)")
	assert.Contains(t, failed, "exit status 1")
}

// Package tfplan reads the plans a `terragrunt run --all --json-out-dir <dir>` run leaves
// — one `terraform show -json` rendering per unit, under <dir>/<unit>/ — and renders the
// markdown summary a reviewer approves: the totals, every destroyed resource, a per-unit
// add/change/destroy table and each changed unit's resource list, destroys first.
package tfplan

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// JSONFile is the name terragrunt gives each unit's `terraform show -json` rendering of
// its plan under --json-out-dir.
const JSONFile = "tfplan.json"

// Planned-change labels, as a reviewer reads them in the summary.
const (
	ActionCreate  = "create"
	ActionUpdate  = "update"
	ActionDelete  = "delete"
	ActionReplace = "replace"
	ActionImport  = "import"
)

// UnitPlan is one unit's planned changes, read from its JSONFile.
type UnitPlan struct {
	// Unit is the unit's path under the plan directory (e.g. "eks").
	Unit string

	// Changes lists the unit's planned resource changes, destroys first.
	Changes []Change

	// Add, Change, and Destroy count the resources the plan creates, updates, and
	// destroys, as Terraform's own "Plan:" line does (a replace is one add and one
	// destroy).
	Add, Change, Destroy int

	// Outputs reports whether the plan changes a root-module output value.
	Outputs bool
}

// Change is one planned change to a resource.
type Change struct {
	// Address is the resource address (aws_s3_bucket.docs, module.x.aws_iam_role.y).
	Address string

	// Action is the change: create, update, delete, replace, import, or Terraform's
	// action list joined with "+" for any other combination (forget, …).
	Action string
}

// HasChanges reports whether applying the plan would change anything: a resource or an
// output.
func (p UnitPlan) HasChanges() bool { return len(p.Changes) > 0 || p.Outputs }

// ReadUnits reads every unit's JSONFile under dir, sorted by unit path. A missing dir
// reads as no units.
func ReadUnits(dir string) ([]UnitPlan, error) {
	var plans []UnitPlan
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != JSONFile {
			return err
		}
		rel, err := filepath.Rel(dir, filepath.Dir(path))
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "locate the unit of "+path)
		}
		p, err := readUnit(path, filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		plans = append(plans, p)
		return nil
	})
	if err != nil && !apperr.StdIs(err, fs.ErrNotExist) {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"read the unit plans under "+dir,
		)
	}
	slices.SortFunc(plans, func(a, b UnitPlan) int { return cmp.Compare(a.Unit, b.Unit) })
	return plans, nil
}

// readUnit parses one unit's plan JSON into its counted changes.
func readUnit(path, unit string) (UnitPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return UnitPlan{}, apperr.Wrap(err, apperr.CodeInternal, "read "+path)
	}
	var doc tfjson.Plan
	if err := json.Unmarshal(data, &doc); err != nil {
		return UnitPlan{}, apperr.Wrap(err, apperr.CodeInternal, "parse "+path)
	}
	p := UnitPlan{Unit: unit}
	for _, rc := range doc.ResourceChanges {
		if rc == nil || rc.Change == nil {
			continue
		}
		action := changeAction(rc.Change.Actions, rc.Change.Importing != nil)
		if action == "" {
			continue
		}
		p.count(action)
		p.Changes = append(p.Changes, Change{Address: rc.Address, Action: action})
	}
	for _, oc := range doc.OutputChanges {
		if oc != nil && !oc.Actions.NoOp() {
			p.Outputs = true
		}
	}
	slices.SortStableFunc(p.Changes, func(a, b Change) int {
		return cmp.Compare(destroyRank(a.Action), destroyRank(b.Action))
	})
	return p, nil
}

// changeAction labels a resource's planned action list, or returns "" for no change
// (no-op, or a data source read).
func changeAction(actions tfjson.Actions, importing bool) string {
	switch {
	case actions.Replace():
		return ActionReplace
	case actions.Create():
		return ActionCreate
	case actions.Update():
		return ActionUpdate
	case actions.Delete():
		return ActionDelete
	case actions.NoOp(), actions.Read():
		if importing {
			return ActionImport
		}
		return ""
	default:
		names := make([]string, 0, len(actions))
		for _, a := range actions {
			names = append(names, string(a))
		}
		return strings.Join(names, "+")
	}
}

// count adds one change with action to the unit's add/change/destroy totals.
func (p *UnitPlan) count(action string) {
	switch action {
	case ActionCreate:
		p.Add++
	case ActionUpdate:
		p.Change++
	case ActionDelete:
		p.Destroy++
	case ActionReplace:
		p.Add++
		p.Destroy++
	}
}

// destroyRank orders destroying changes (delete, replace) before the rest.
func destroyRank(action string) int {
	if action == ActionDelete || action == ActionReplace {
		return 0
	}
	return 1
}

// Summary renders the reviewer's markdown summary of the unit plans under title: the
// totals, every destroyed resource, a per-unit add/change/destroy table, and each
// changed unit's resource list. Units that destroy come first, then units with other
// changes, then unchanged units.
func Summary(title string, plans []UnitPlan) string {
	plans = slices.Clone(plans)
	slices.SortStableFunc(plans, func(a, b UnitPlan) int {
		if c := cmp.Compare(b.Destroy, a.Destroy); c != 0 {
			return c
		}
		return cmp.Compare(changeRank(a), changeRank(b))
	})
	var add, change, destroy int
	for _, p := range plans {
		add, change, destroy = add+p.Add, change+p.Change, destroy+p.Destroy
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n\n", title)
	if len(plans) == 0 {
		b.WriteString("No unit has pending changes.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%d unit(s): **%d to add, %d to change, %d to destroy.**\n\n",
		len(plans), add, change, destroy)
	writeDestroys(&b, plans)
	b.WriteString("| Unit | Add | Change | Destroy |\n|---|---:|---:|---:|\n")
	for _, p := range plans {
		unit := p.Unit
		if p.Outputs {
			unit += " (outputs change)"
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d |\n", cell(unit), p.Add, p.Change, p.Destroy)
	}
	for _, p := range plans {
		writeUnitChanges(&b, p)
	}
	return b.String()
}

// changeRank orders a unit with resource changes before one without.
func changeRank(p UnitPlan) int {
	if len(p.Changes) > 0 {
		return 0
	}
	return 1
}

// writeDestroys writes the "Destroys" section listing every deleted or replaced
// resource; it writes nothing when the plans destroy nothing.
func writeDestroys(b *strings.Builder, plans []UnitPlan) {
	var rows []string
	for _, p := range plans {
		for _, c := range p.Changes {
			if destroyRank(c.Action) == 0 {
				rows = append(rows, fmt.Sprintf("| %s | %#q | %s |",
					cell(p.Unit), cell(c.Address), c.Action))
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	b.WriteString("#### Destroys\n\n| Unit | Resource | Action |\n|---|---|---|\n")
	b.WriteString(strings.Join(rows, "\n"))
	b.WriteString("\n\n")
}

// writeUnitChanges writes one unit's changed resources as a collapsed list.
func writeUnitChanges(b *strings.Builder, p UnitPlan) {
	if len(p.Changes) == 0 {
		return
	}
	fmt.Fprintf(b, "\n<details><summary>%s: %d change(s)</summary>\n\n",
		p.Unit, len(p.Changes))
	for _, c := range p.Changes {
		fmt.Fprintf(b, "- %s `%s`\n", c.Action, c.Address)
	}
	b.WriteString("\n</details>\n")
}

// FailedSummary is the summary a failed plan leaves in place of the unit tables, so a
// PR comment or job summary says why there is no plan.
func FailedSummary(title string, err error) string {
	return fmt.Sprintf(
		"### %s (failed)\n\nThe plan did not complete, so there is no plan "+
			"to review or apply:\n\n```\n%s\n```\n\nSee the workflow run log for the "+
			"terragrunt output.\n",
		title, err.Error(),
	)
}

// cell escapes a value for a markdown table cell.
func cell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

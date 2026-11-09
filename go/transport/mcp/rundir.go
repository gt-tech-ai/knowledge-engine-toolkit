package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// groupDirMode is the mode of a directory its group may read.
const groupDirMode = 0o750

// privateFileMode is the mode of a file only its owner reads.
const privateFileMode = 0o600

// runIDLayout formats a run id: a UTC timestamp to the nanosecond, so ids sort
// in the order the runs started.
const runIDLayout = "20060102T150405.000000000Z"

// RunDir is the directory holding one subdirectory of step logs per tool call.
type RunDir struct {
	// now stamps each run id.
	now func() time.Time

	// root is the directory the run subdirectories live in.
	root string
}

// NewRunDir returns the run directory rooted at root, stamping run ids with
// now (the wall clock when nil).
func NewRunDir(root string, now func() time.Time) RunDir {
	if now == nil {
		now = time.Now
	}

	return RunDir{root: root, now: now}
}

// NewRunID returns a fresh run id: the current UTC time in runIDLayout.
func (d RunDir) NewRunID() string {
	return d.now().UTC().Format(runIDLayout)
}

// WriteSteps writes each step's output to <root>/<runID>/<step>.log
// (owner-only) and returns the file paths aligned with steps: "" for a step
// with no output, which gets no file. Characters outside [A-Za-z0-9_.-] in a
// step name become "_", and a name already used in the run gets the first free
// "-<n>" suffix, so same-named steps keep distinct files and paths. On a write failure
// the paths written so far are returned with the error.
func (d RunDir) WriteSteps(
	runID string,
	steps []types.StepResult,
) ([]string, error) {
	logs := make([]string, len(steps))
	dir := filepath.Join(d.root, runID)

	hasDetail := func(s types.StepResult) bool { return len(s.Detail) > 0 }
	if !slices.ContainsFunc(steps, hasDetail) {
		return logs, nil
	}

	err := os.MkdirAll(dir, groupDirMode)
	if err != nil {
		return logs, apperr.Wrap(err, apperr.CodeInternal, "mcp: create run dir")
	}

	used := map[string]bool{}

	for i := range steps {
		if len(steps[i].Detail) == 0 {
			continue
		}

		logs[i], err = writeStepLog(dir, &steps[i], used)
		if err != nil {
			return logs, err
		}
	}

	return logs, nil
}

// writeStepLog stores one step's detail under a name unique within the run.
func writeStepLog(dir string, s *types.StepResult, used map[string]bool) (string, error) {
	base := safeName(s.Name)
	name := base

	for n := 2; used[name]; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}

	used[name] = true

	path := filepath.Join(dir, name+".log")

	err := os.WriteFile(path, s.Detail, privateFileMode)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, fmt.Sprintf(
			"mcp: write step log %q", s.Name,
		))
	}

	return path, nil
}

// Prune removes all but the keep newest run directories. Only entries named
// like a run id are considered, so nothing else under root is touched; a
// missing root is empty.
func (d RunDir) Prune(keep int) error {
	entries, err := os.ReadDir(d.root)
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "mcp: read run dir")
	}

	runs := runDirNames(entries)

	for _, name := range runs[:max(len(runs)-keep, 0)] {
		err := os.RemoveAll(filepath.Join(d.root, name))
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, fmt.Sprintf(
				"mcp: prune run %q", name,
			))
		}
	}

	return nil
}

// runDirNames keeps the directories named like a run id, in directory order.
func runDirNames(entries []os.DirEntry) []string {
	var runs []string

	for _, e := range entries {
		_, perr := time.Parse(runIDLayout, e.Name())
		if e.IsDir() && perr == nil {
			runs = append(runs, e.Name())
		}
	}

	return runs
}

// safeName maps every character outside [A-Za-z0-9_.-] in name to "_".
func safeName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			return r
		}

		return '_'
	}, name)
}

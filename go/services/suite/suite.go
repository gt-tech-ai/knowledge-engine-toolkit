// Package suite builds the gate of a suite-backed command from data: a types.Suite
// selects entries of a catalog (tool runs, guards, procedures and registered steps) by
// flag, and runs one decorated job per entry in the suite's phases.
package suite

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/gate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/job"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// KindStep marks a registered-step entry: the suite runs it only when its Steps list
// names it.
const KindStep = "step"

// Entry is one catalog item a suite can select.
type Entry struct {
	// Job builds the entry's job under env.
	Job func(env Env) interfaces.AnyJob

	// Name identifies the entry; selections match it.
	Name string

	// Verb is the command that owns the entry (lint, check); selections match it.
	Verb string

	// Group is the phase the entry runs in.
	Group string

	// Kind is the entry's source (run, guard, procedure, step).
	Kind string

	// Tags are the labels selections match.
	Tags []string
}

// Env is what every entry's job is built against.
type Env struct {
	// Decs decorates each job.
	Decs decorate.Decorations

	// Snapshot memoizes per-invocation work the jobs share.
	Snapshot *codebase.Snapshot

	// Component prefixes each job's decoration component (<component>.<verb>.<name>).
	Component string

	// Opts are the run options every entry runs under.
	Opts types.RunOptions
}

// PlannedPhase is a suite phase with the selected entries it runs.
type PlannedPhase struct {
	// Group is the phase name.
	Group string

	// Entries are the phase's entries, in catalog order.
	Entries []Entry

	// Serial runs them one at a time.
	Serial bool
}

// Select returns the catalog entries the flags select, in catalog order: the union of the
// set selector flags (the suite's Select when none is set), narrowed by every set
// modifier flag, without the entries whose gated tag's flag is unset, and without the
// step entries the suite's Steps does not name. A flag the suite does not declare, or a
// listed step the catalog lacks, is CodeInvalidInput.
func Select(s types.Suite, catalog []Entry, flags []string) ([]Entry, error) {
	var sels, mods []types.Selection
	for _, f := range flags {
		if sel, ok := s.Selectors[f]; ok {
			sels = append(sels, sel)
			continue
		}
		if mod, ok := s.Modifiers[f]; ok {
			mods = append(mods, mod)
			continue
		}
		return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"suite %s has no flag %q (known: %s)", s.Title, f, flagNames(s)))
	}
	for _, name := range s.Steps {
		if !slices.ContainsFunc(catalog, func(e Entry) bool {
			return e.Kind == KindStep && e.Name == name
		}) {
			return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"suite %s: no registered step %q", s.Title, name))
		}
	}
	if len(sels) == 0 {
		sels = []types.Selection{s.Select}
	}
	var out []Entry
	for _, e := range catalog {
		if e.Kind == KindStep && !slices.Contains(s.Steps, e.Name) {
			continue
		}
		if matchesAny(e, sels) && matchesAll(e, mods) && ungated(s, e, flags) {
			out = append(out, e)
		}
	}
	return out, nil
}

// Plan groups entries into the suite's phases, in Phases order, dropping a phase with no
// entry. An entry whose group names no phase is CodeInvalidInput, so an entry can never
// silently not run.
func Plan(s types.Suite, entries []Entry) ([]PlannedPhase, error) {
	byGroup := map[string][]Entry{}
	for _, e := range entries {
		if !slices.ContainsFunc(
			s.Phases,
			func(p types.Phase) bool { return p.Group == e.Group },
		) {
			return nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"suite %s: %s is in group %q, which no phase names",
				s.Title,
				e.Name,
				e.Group,
			))
		}
		byGroup[e.Group] = append(byGroup[e.Group], e)
	}
	var out []PlannedPhase
	for _, p := range s.Phases {
		if es := byGroup[p.Group]; len(es) > 0 {
			out = append(out, PlannedPhase{Group: p.Group, Entries: es, Serial: p.Serial})
		}
	}
	return out, nil
}

// Gate composes one suite's gate.
type Gate struct {
	// Obs receives the gate's progress (may be nil).
	Obs interfaces.ExecutionObserver

	// Err is a composition failure (no such suite, no codebase): the gate is one failing
	// job reporting it.
	Err error

	// Env is what every entry's job is built against; a nil Snapshot gets a fresh one.
	Env Env

	// Suite is the suite definition.
	Suite types.Suite

	// Catalog is every entry the suite may select.
	Catalog []Entry

	// Flags are the set selector and modifier flag names.
	Flags []string
}

var _ gate.GateFactory = Gate{}

// Gate builds the suite's gate: one job per selected entry, in phase order; a Serial
// phase runs its jobs one at a time. A suite that cannot be planned is one failing job.
func (g Gate) Gate() *gate.Gate {
	b := gate.New(g.Suite.Title)
	if g.Obs != nil {
		b.Observe(g.Obs)
	}
	phases, err := g.phases()
	if err != nil {
		b.Serial(g.Suite.Title, failed(g.Suite.Title, err))
		return b.Decorate(g.Env.Decs)
	}
	for _, p := range phases {
		if p.serial {
			b.Serial(p.group, p.jobs...)
		} else {
			b.Parallel(p.group, p.jobs...)
		}
	}
	return b.Decorate(g.Env.Decs)
}

// Jobs returns every job of the suite's gate in phase order, for a caller that folds the
// suite into a phase of its own; a suite that cannot be planned is one failing job.
func (g Gate) Jobs() []interfaces.AnyJob {
	phases, err := g.phases()
	if err != nil {
		return []interfaces.AnyJob{failed(g.Suite.Title, err)}
	}
	var jobs []interfaces.AnyJob
	for _, p := range phases {
		jobs = append(jobs, p.jobs...)
	}
	return jobs
}

// phase is one planned phase's jobs.
type phase struct {
	// group names the phase.
	group string

	// jobs are the phase's jobs.
	jobs []interfaces.AnyJob

	// serial runs the jobs one at a time.
	serial bool
}

// phases selects, plans and builds each phase's jobs.
func (g Gate) phases() ([]phase, error) {
	if g.Err != nil {
		return nil, g.Err
	}
	selected, err := Select(g.Suite, g.Catalog, g.Flags)
	if err != nil {
		return nil, err
	}
	planned, err := Plan(g.Suite, selected)
	if err != nil {
		return nil, err
	}
	env := g.Env
	if env.Snapshot == nil {
		env.Snapshot = codebase.NewSnapshot(strings.ToLower(g.Suite.Title))
	}
	out := make([]phase, 0, len(planned))
	for _, p := range planned {
		jobs := make([]interfaces.AnyJob, 0, len(p.Entries))
		for _, e := range p.Entries {
			jobs = append(jobs, e.Job(env))
		}
		out = append(out, phase{group: p.Group, jobs: jobs, serial: p.Serial})
	}
	return out, nil
}

// Build decorates run as the job name in group, under the component
// <env.Component>.<verb>.<name>; a decoration that fails to resolve becomes the job's
// error. Entry sources use it so every suite job is decorated the same way.
func Build(env Env, verb, name, group string, run job.Run) interfaces.AnyJob {
	prefix := env.Component
	if prefix == "" {
		prefix = "suite"
	}
	b, err := job.NewBase(env.Decs, job.Component(prefix, verb, name))
	if err != nil {
		return job.Base{}.Func(name, group, func(
			context.Context, interfaces.CommandRunner, string,
		) (types.StepResults, error) {
			return nil, err
		})
	}
	return b.Func(name, group, run)
}

// failed is the one-step job that fails with err.
func failed(name string, err error) interfaces.AnyJob {
	return job.Base{}.Check(name, name, func(context.Context) error { return err })
}

// matchesAny reports whether e matches one of sels.
func matchesAny(e Entry, sels []types.Selection) bool {
	return slices.ContainsFunc(sels, func(s types.Selection) bool {
		return s.Matches(e.Name, e.Verb, e.Tags)
	})
}

// matchesAll reports whether e matches every modifier selection.
func matchesAll(e Entry, mods []types.Selection) bool {
	return !slices.ContainsFunc(mods, func(s types.Selection) bool {
		return !s.Matches(e.Name, e.Verb, e.Tags)
	})
}

// ungated reports whether every gated tag e carries has its flag among flags.
func ungated(s types.Suite, e Entry, flags []string) bool {
	return !slices.ContainsFunc(e.Tags, func(tag string) bool {
		flag, ok := s.Gated[tag]
		return ok && !slices.Contains(flags, flag)
	})
}

// flagNames lists the suite's selector and modifier flags, sorted.
func flagNames(s types.Suite) string {
	names := make([]string, 0, len(s.Selectors)+len(s.Modifiers))
	for k := range s.Selectors {
		names = append(names, k)
	}
	for k := range s.Modifiers {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

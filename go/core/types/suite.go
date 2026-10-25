package types

import "slices"

// Suite is one suite-backed command: the catalog entries (tool runs, guards, procedures,
// registered steps) it selects, how its flags narrow them, and the phases they run in.
type Suite struct {
	// Selectors maps a selector flag (go, infra, …) to the entries it adds; several
	// selector flags union.
	Selectors map[string]Selection `yaml:"selectors,omitempty" json:"selectors,omitempty"`

	// Modifiers maps a modifier flag (fast, offline) to a selection every entry must also
	// match; modifiers intersect with the selected entries.
	Modifiers map[string]Selection `yaml:"modifiers,omitempty" json:"modifiers,omitempty"`

	// Gated maps a tag to the flag it needs: an entry carrying the tag runs only when that
	// flag is set, since a modifier can only narrow.
	Gated map[string]string `yaml:"gated,omitempty" json:"gated,omitempty"`

	// Title is the gate title.
	Title string `yaml:"title" json:"title"`

	// Phases orders the entry groups; a group the selection reaches must be listed.
	Phases []Phase `yaml:"phases" json:"phases"`

	// Steps names the registered steps the suite runs besides its rows.
	Steps []string `yaml:"steps,omitempty" json:"steps,omitempty"`

	// Select is the selection when no selector flag is set.
	Select Selection `yaml:"select" json:"select"`
}

// Phase is one gate phase of a suite: the entries whose group equals Group.
type Phase struct {
	// Group is the entry group the phase runs, and the phase's name.
	Group string `yaml:"group" json:"group"`

	// Serial runs the phase's entries one at a time in catalog order; set it when they
	// write files they share (formatters, lint --fix).
	Serial bool `yaml:"serial,omitempty" json:"serial,omitempty"`
}

// Matches reports whether an entry named name, of verb, carrying tags, satisfies every
// constraint s sets.
func (s Selection) Matches(name, verb string, tags []string) bool {
	has := func(tag string) bool { return slices.Contains(tags, tag) }
	switch {
	case len(s.Verbs) > 0 && !slices.Contains(s.Verbs, verb),
		len(s.Names) > 0 && !slices.Contains(s.Names, name),
		slices.Contains(s.Exclude, name),
		slices.ContainsFunc(s.Tags, func(t string) bool { return !has(t) }),
		len(s.AnyTags) > 0 && !slices.ContainsFunc(s.AnyTags, has),
		slices.ContainsFunc(s.ExcludeTags, has):
		return false
	}
	return true
}

// Composite is an ordered sequence of phases over suites and named jobs: a command whose
// levels are unions of suites (a preflight) is one Composite per level, and the order
// of its phases is the data.
type Composite struct {
	// Title is the gate title.
	Title string `yaml:"title" json:"title"`

	// Phases are the gate's phases, in order.
	Phases []CompositePhase `yaml:"phases" json:"phases"`
}

// CompositePhase is one phase of a Composite: the suites it folds in, each under its own
// flags, then the jobs the product registers by name. A phase that builds no job (a
// suite selecting nothing, a job the product withholds) is dropped.
type CompositePhase struct {
	// Group is the phase name.
	Group string `yaml:"group" json:"group"`

	// Suites are the suites whose jobs the phase runs.
	Suites []SuiteRef `yaml:"suites,omitempty" json:"suites,omitempty"`

	// Jobs names the product-registered jobs the phase runs after the suites' jobs.
	Jobs []string `yaml:"jobs,omitempty" json:"jobs,omitempty"`

	// Serial runs the phase's jobs one at a time, in order.
	Serial bool `yaml:"serial,omitempty" json:"serial,omitempty"`
}

// SuiteRef names a suite and how a composite runs it.
type SuiteRef struct {
	// Select, when set, replaces the suite's default selection (one guard of the check
	// suite, say).
	Select *Selection `yaml:"select,omitempty" json:"select,omitempty"`

	// Name is the suite's name in the product's suites.
	Name string `yaml:"name" json:"name"`

	// Flags are the suite's selector and modifier flags to set.
	Flags []string `yaml:"flags,omitempty" json:"flags,omitempty"`

	// Strict fails a run whose tool is absent instead of skipping it, and makes every
	// finding a failure.
	Strict bool `yaml:"strict,omitempty" json:"strict,omitempty"`
}

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

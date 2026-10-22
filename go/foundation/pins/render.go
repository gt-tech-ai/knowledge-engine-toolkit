package pins

import (
	"bytes"
	"encoding/json"
	"slices"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// schemaURL is the Renovate JSON-schema URL written as $schema.
const schemaURL = "https://docs.renovatebot.com/renovate-schema.json"

// ConfigFileName is the conventional name of the config at the codebase root.
const ConfigFileName = "renovate.json"

// Config models renovate.json. JSON key order follows field order; Renovate ignores
// it, and the drift check compares rendered bytes, which are deterministic.
type Config struct {
	// LockFileMaintenance enables scheduled lockfile refresh (nil to omit).
	LockFileMaintenance *LockFileMaintenance `json:"lockFileMaintenance,omitempty"`

	// VulnerabilityAlerts configures security-advisory PRs (nil to omit).
	VulnerabilityAlerts *VulnerabilityAlerts `json:"vulnerabilityAlerts,omitempty"`

	// Schema is the Renovate JSON-schema URL ($schema).
	Schema string `json:"$schema"`

	// Timezone is the IANA timezone the schedules are evaluated in.
	Timezone string `json:"timezone"`

	// Extends lists the shared Renovate presets this config inherits.
	Extends []string `json:"extends"`

	// Labels are applied to every Renovate PR.
	Labels []string `json:"labels"`

	// Schedule constrains when Renovate may open PRs.
	Schedule []string `json:"schedule"`

	// PackageRules are the per-manager and per-package rules, in merge order.
	PackageRules []PackageRule `json:"packageRules"`

	// CustomManagers are regex managers for versions the built-in managers miss
	// (omitted when empty).
	CustomManagers []CustomManager `json:"customManagers,omitempty"`

	// IgnorePaths are path globs Renovate skips (omitted when empty).
	IgnorePaths []string `json:"ignorePaths,omitempty"`

	// PRHourlyLimit caps PRs opened per hour (0 = unlimited).
	PRHourlyLimit int `json:"prHourlyLimit"`

	// PRConcurrentLimit caps simultaneously-open PRs (0 = unlimited).
	PRConcurrentLimit int `json:"prConcurrentLimit"`
}

// LockFileMaintenance enables scheduled refresh of lockfiles so they don't drift
// between explicit dependency bumps.
type LockFileMaintenance struct {
	// Enabled turns scheduled lockfile maintenance on.
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// PackageRule is one Renovate packageRules entry. The pointer booleans keep an
// explicit false in the output, which a value with omitempty would drop.
type PackageRule struct {
	// MatchManagers scopes the rule to managers (e.g. "gomod").
	MatchManagers []string `json:"matchManagers,omitempty" yaml:"matchManagers"`

	// MatchUpdateTypes scopes the rule to update kinds (e.g. "minor", "patch").
	MatchUpdateTypes []string `json:"matchUpdateTypes,omitempty" yaml:"matchUpdateTypes"`

	// MatchPackageNames scopes the rule to package names or globs.
	MatchPackageNames []string `json:"matchPackageNames,omitempty" yaml:"matchPackageNames"`

	// MatchDatasources scopes the rule to datasources (e.g. "golang-version").
	MatchDatasources []string `json:"matchDatasources,omitempty" yaml:"matchDatasources"`

	// AllowedVersions bounds which versions Renovate may propose (a semver range or
	// a regex).
	AllowedVersions string `json:"allowedVersions,omitempty" yaml:"allowedVersions"`

	// GroupName batches matched updates into one named PR.
	GroupName string `json:"groupName,omitempty" yaml:"groupName"`

	// PostUpdateOptions are post-update commands (e.g. "gomodTidy").
	PostUpdateOptions []string `json:"postUpdateOptions,omitempty" yaml:"postUpdateOptions"`

	// Automerge enables auto-merge for matched updates.
	Automerge *bool `json:"automerge,omitempty" yaml:"automerge"`

	// Enabled toggles the matched updates; false holds them.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled"`

	// AutomergeType selects how auto-merge lands (e.g. "pr", "branch").
	AutomergeType string `json:"automergeType,omitempty" yaml:"automergeType"`

	// Labels are applied to PRs this rule matches.
	Labels []string `json:"labels,omitempty" yaml:"labels"`
}

// VulnerabilityAlerts configures security-advisory PRs.
type VulnerabilityAlerts struct {
	// Labels are applied to security-advisory PRs.
	Labels []string `json:"labels" yaml:"labels"`

	// Enabled turns vulnerability-alert PRs on.
	Enabled bool `json:"enabled" yaml:"enabled"`
}

// CustomManager is a Renovate regex custom-manager entry, for versions the built-in
// managers do not see (a tool pinned in a shell step, an image digest outside a
// Compose file).
type CustomManager struct {
	// CustomType selects the custom-manager kind ("regex").
	CustomType string `json:"customType" yaml:"customType"`

	// DatasourceTemplate is the datasource new versions are looked up in.
	DatasourceTemplate string `json:"datasourceTemplate,omitempty" yaml:"datasourceTemplate,omitempty"`

	// ManagerFilePatterns are the files this manager scans, each a regex wrapped in
	// slashes (a bare entry would be a glob).
	ManagerFilePatterns []string `json:"managerFilePatterns,omitempty" yaml:"managerFilePatterns,omitempty"`

	// MatchStrings are the regexes that capture depName and currentValue (and
	// currentDigest for a digest-pinned image).
	MatchStrings []string `json:"matchStrings,omitempty" yaml:"matchStrings,omitempty"`
}

// Gated is a policy entry that applies only when every manager in When is present.
type Gated[T any] struct {
	// Value is the entry.
	Value T `yaml:"value"`

	// When lists the managers that must all be present; empty always applies.
	When []Manager `yaml:"when"`
}

// Policy is a consumer's update policy: everything in the config that is not derived
// from the detected ecosystems. Gated entries keep their order.
type Policy struct {
	// LockFileMaintenance is copied to the config (nil to omit).
	LockFileMaintenance *LockFileMaintenance `yaml:"lockFileMaintenance"`

	// VulnerabilityAlerts is copied to the config (nil to omit).
	VulnerabilityAlerts *VulnerabilityAlerts `yaml:"vulnerabilityAlerts"`

	// Timezone is the IANA timezone the schedule is evaluated in.
	Timezone string `yaml:"timezone"`

	// Extends lists the shared presets the config inherits.
	Extends []string `yaml:"extends"`

	// Labels are applied to every PR.
	Labels []string `yaml:"labels"`

	// Schedule constrains when PRs may open.
	Schedule []string `yaml:"schedule"`

	// GroupRules follow the per-manager rules and precede the global major rule,
	// so they override a manager's grouping.
	GroupRules []Gated[PackageRule] `yaml:"groupRules"`

	// PinRules follow the global major rule, so their enabled: false holds updates
	// the earlier rules would allow (Renovate merges matching rules in order).
	PinRules []Gated[PackageRule] `yaml:"pinRules"`

	// CustomManagers are the regex managers to emit.
	CustomManagers []Gated[CustomManager] `yaml:"customManagers"`

	// IgnorePaths are path globs to skip beyond the detected ones.
	IgnorePaths []Gated[string] `yaml:"ignorePaths"`

	// PRHourlyLimit caps PRs opened per hour (0 = unlimited).
	PRHourlyLimit int `yaml:"prHourlyLimit"`

	// PRConcurrentLimit caps simultaneously-open PRs (0 = unlimited).
	PRConcurrentLimit int `yaml:"prConcurrentLimit"`
}

// Render serializes the config for e under p: one rule per present manager in sorted
// order, p's group rules, a rule that never auto-merges a major update, p's pin rules,
// and a rule that disables lookups of the codebase's own Go workspace modules. The
// output is deterministic: two-space indent, no HTML escaping, one trailing newline.
func Render(e EcosystemSet, p Policy) ([]byte, error) {
	return marshal(Build(e, p))
}

// Build assembles the Config Render serializes.
func Build(e EcosystemSet, p Policy) Config {
	mgrs := slices.Clone(e.Managers)
	slices.Sort(mgrs)
	var rules []PackageRule
	for _, m := range mgrs {
		if r, ok := managerRule(m); ok {
			rules = append(rules, r)
		}
	}
	rules = append(rules, gated(e, p.GroupRules)...)
	rules = append(rules, globalMajorRule())
	rules = append(rules, gated(e, p.PinRules)...)
	rules = append(rules, workspaceModuleRules(e)...)

	ignore := slices.Concat(e.IgnorePaths, gated(e, p.IgnorePaths))
	slices.Sort(ignore)
	return Config{
		Schema:              schemaURL,
		Extends:             slices.Clone(p.Extends),
		Labels:              slices.Clone(p.Labels),
		PRHourlyLimit:       p.PRHourlyLimit,
		PRConcurrentLimit:   p.PRConcurrentLimit,
		Schedule:            slices.Clone(p.Schedule),
		Timezone:            p.Timezone,
		LockFileMaintenance: p.LockFileMaintenance,
		PackageRules:        rules,
		CustomManagers:      gated(e, p.CustomManagers),
		IgnorePaths:         slices.Compact(ignore),
		VulnerabilityAlerts: p.VulnerabilityAlerts,
	}
}

// gated returns the values of entries whose managers are all present in e.
func gated[T any](e EcosystemSet, entries []Gated[T]) []T {
	var out []T
	for _, g := range entries {
		if e.HasAll(g.When) {
			out = append(out, g.Value)
		}
	}
	return out
}

// managerRule returns the default rule for a manager: one grouped PR per ecosystem,
// never auto-merged, except GitHub Actions minor and patch bumps, which auto-merge.
func managerRule(m Manager) (PackageRule, bool) {
	group := map[Manager]string{
		ManagerGomod:         "Go dependencies",
		ManagerPEP621:        "Python dependencies",
		ManagerNpm:           "Node.js dependencies",
		ManagerDockerfile:    "Docker base images",
		ManagerDockerCompose: "Docker Compose images",
		ManagerTerraform:     "Terraform providers",
		ManagerTerragrunt:    "Terragrunt modules",
		ManagerHelmv3:        "Helm charts",
	}
	switch {
	case m == ManagerGithubActions:
		return PackageRule{
			MatchManagers:    []string{string(m)},
			MatchUpdateTypes: []string{"minor", "patch"},
			GroupName:        "GitHub Actions",
			Automerge:        new(true),
			AutomergeType:    "pr",
		}, true
	case m == ManagerGomod:
		return PackageRule{
			MatchManagers:     []string{string(m)},
			GroupName:         group[m],
			PostUpdateOptions: []string{"gomodTidy"},
			Automerge:         new(false),
		}, true
	case group[m] != "":
		return PackageRule{
			MatchManagers: []string{string(m)},
			GroupName:     group[m],
			Automerge:     new(false),
		}, true
	}
	return PackageRule{}, false
}

// globalMajorRule labels every major update and never auto-merges it.
func globalMajorRule() PackageRule {
	return PackageRule{
		MatchPackageNames: []string{"*"},
		MatchUpdateTypes:  []string{"major"},
		Automerge:         new(false),
		Labels:            []string{"dependencies", "major"},
	}
}

// workspaceModuleRules disables lookups of the codebase's own Go workspace modules
// (every package under the root module path): they resolve through go.work, never a
// module proxy, so a lookup only fails. Emitted when Go is present and the root module
// path is known.
func workspaceModuleRules(e EcosystemSet) []PackageRule {
	if !e.Has(ManagerGomod) || e.GoModule == "" {
		return nil
	}
	return []PackageRule{{
		MatchManagers:     []string{string(ManagerGomod)},
		MatchPackageNames: []string{e.GoModule + "/**"},
		Enabled:           new(false),
	}}
}

// marshal is the canonical serializer: two-space indent, no HTML escaping, exactly one
// trailing newline.
func marshal(cfg Config) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "marshal renovate config")
	}
	return buf.Bytes(), nil
}

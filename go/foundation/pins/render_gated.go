package pins

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

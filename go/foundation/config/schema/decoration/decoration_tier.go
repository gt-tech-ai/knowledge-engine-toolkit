package decoration

import (
	"maps"
	"reflect"
)

// Tier is one tier's decoration: the Defaults every component gets and the Overrides
// one named component lays over them.
type Tier struct {
	// Overrides maps a component name to the layers it lays over Defaults.
	Overrides map[string]Layers `mapstructure:"overrides"`

	// Defaults are every component's layers.
	Defaults Layers `mapstructure:"defaults"`
}

// For returns the layers of the component named name: Defaults, then the override of
// each dotted prefix of name from the shortest, then name's own override, each laid on
// the last. "api.documents.create" gets Defaults, "api", "api.documents", then its own,
// so one key covers a group and a deeper key one component.
func (t *Tier) For(name string) Layers {
	tCopy := *t

	return Merge(&tCopy.Defaults, new(tCopy.Overriding(name)))
}

// Overriding returns the layers the overrides lay over Defaults for the component named
// name: each dotted prefix's override from the shortest, then name's own.
func (t *Tier) Overriding(name string) Layers {
	var l Layers

	for i := range len(name) {
		if name[i] == '.' {
			l = Merge(&l, new(t.Overrides[name[:i]]))
		}
	}

	return Merge(&l, new(t.Overrides[name]))
}

// Sources returns the config keys For merges for the component named name, in order:
// "defaults" when the tier has defaults, then "overrides.<key>" for each dotted prefix of
// name and for name itself that has an override.
func (t *Tier) Sources(name string) []string {
	var out []string

	if !reflect.ValueOf(t.Defaults).IsZero() {
		out = append(out, "defaults")
	}

	for _, key := range dottedPrefixes(name) {
		if _, ok := t.Overrides[key]; ok {
			out = append(out, "overrides."+key)
		}
	}

	return out
}

// overlay lays over on t: its defaults over t's, and each override over t's override
// of the same name.
func (t *Tier) overlay(over *Tier) Tier {
	tCopy := *t

	out := Tier{Defaults: Merge(&tCopy.Defaults, &over.Defaults)}
	if tCopy.Overrides == nil && over.Overrides == nil {
		return out
	}

	out.Overrides = make(map[string]Layers, len(tCopy.Overrides)+len(over.Overrides))
	maps.Copy(out.Overrides, tCopy.Overrides)

	for name := range over.Overrides {
		out.Overrides[name] = Merge(new(out.Overrides[name]), new(over.Overrides[name]))
	}

	return out
}

// Config is the decoration of every tier.
type Config struct {
	// Controllers decorates the controller (transport-handler) tier
	// (decoration.controllers).
	Controllers Tier `mapstructure:"controllers"`

	// Clients decorates the clients tier (decoration.clients).
	Clients Tier `mapstructure:"clients"`

	// Workflows decorates the workflow tier (decoration.workflows).
	Workflows Tier `mapstructure:"workflows"`

	// Pipelines decorates the pipeline tier (decoration.pipelines).
	Pipelines Tier `mapstructure:"pipelines"`

	// Services decorates the service tier (decoration.services).
	Services Tier `mapstructure:"services"`

	// Repositories decorates the repository tier (decoration.repositories).
	Repositories Tier `mapstructure:"repositories"`

	// Stores decorates the store tier (decoration.stores).
	Stores Tier `mapstructure:"stores"`

	// Jobs decorates the execution-job tier (decoration.jobs).
	Jobs Tier `mapstructure:"jobs"`

	// Gates decorates the execution-gate tier (decoration.gates).
	Gates Tier `mapstructure:"gates"`
}

// Section is one tier's decoration under its config key.
type Section struct {
	// Key is the tier's config key under decoration ("repositories").
	Key string

	// Name is the tier's name in code ("repository"), as decorate.Tier names it.
	Name string

	// Tier is the tier's decoration.
	Tier Tier
}

// Sections returns every tier's decoration under its config key, in config order.
func (c *Config) Sections() []Section {
	return []Section{
		{Key: "controllers", Name: "controller", Tier: c.Controllers},
		{Key: "clients", Name: "client", Tier: c.Clients},
		{Key: "workflows", Name: "workflow", Tier: c.Workflows},
		{Key: "pipelines", Name: "pipeline", Tier: c.Pipelines},
		{Key: "services", Name: "service", Tier: c.Services},
		{Key: "repositories", Name: "repository", Tier: c.Repositories},
		{Key: "stores", Name: "store", Tier: c.Stores},
		{Key: "jobs", Name: "job", Tier: c.Jobs},
		{Key: "gates", Name: "gate", Tier: c.Gates},
	}
}

// Tier returns the decoration of the tier named name ("controller", "client",
// "workflow", "pipeline", "service", "repository", "store", "job", "gate"), and whether
// there is one.
func (c *Config) Tier(name string) (Tier, bool) {
	sections := c.Sections()
	for i := range sections {
		if sections[i].Name == name {
			return sections[i].Tier, true
		}
	}

	return Tier{}, false
}

// Overlay lays over on c, tier by tier (see Tier.overlay).
func (c *Config) Overlay(over *Config) Config {
	return Config{
		Controllers:  c.Controllers.overlay(&over.Controllers),
		Clients:      c.Clients.overlay(&over.Clients),
		Workflows:    c.Workflows.overlay(&over.Workflows),
		Pipelines:    c.Pipelines.overlay(&over.Pipelines),
		Services:     c.Services.overlay(&over.Services),
		Repositories: c.Repositories.overlay(&over.Repositories),
		Stores:       c.Stores.overlay(&over.Stores),
		Jobs:         c.Jobs.overlay(&over.Jobs),
		Gates:        c.Gates.overlay(&over.Gates),
	}
}

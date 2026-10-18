package decorate

import (
	"maps"
	"slices"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
)

// Component is one component Decorations resolved: its tier and its name.
type Component struct {
	// Tier is the tier's name ("repository", "client", …).
	Tier string

	// Name is the component's dotted name.
	Name string
}

// Components returns every component d resolved, ordered by tier then name: the
// inventory a generator writes one decoration entry for. The zero Decorations has none.
func (d Decorations) Components() []Component {
	if d.reg == nil {
		return nil
	}
	d.reg.mu.Lock()
	defer d.reg.mu.Unlock()
	var out []Component
	for _, tier := range slices.Sorted(maps.Keys(d.reg.used)) {
		for _, name := range slices.Sorted(maps.Keys(d.reg.used[tier])) {
			out = append(out, Component{Tier: tier, Name: name})
		}
	}
	return out
}

// VerifyExplicit is Verify, and also fails CodeInvalidInput naming every resolved
// component under prefixes that has no override of its own name: in a deployable whose
// decoration file is generated with one entry per component, a component without one was
// added after the file was generated, and takes only its tier's defaults.
func (d Decorations) VerifyExplicit(prefixes ...string) error {
	err := d.Verify(prefixes...)
	if d.reg == nil {
		return err
	}
	cfg := d.cfg
	if cfg == nil {
		cfg = &decoration.Config{}
	}
	keys := map[string]string{}
	overrides := map[string]map[string]decoration.Layers{}
	sections := cfg.Sections()
	for i := range sections {
		keys[sections[i].Name] = sections[i].Key
		overrides[sections[i].Name] = sections[i].Tier.Overrides
	}
	var missing []string
	for _, c := range d.Components() {
		if !owned(c.Name, prefixes) {
			continue
		}
		if _, ok := overrides[c.Tier][c.Name]; !ok {
			missing = append(
				missing,
				decoration.SectionKey+"."+keys[c.Tier]+".overrides."+c.Name,
			)
		}
	}
	if len(missing) == 0 {
		return err
	}
	miss := apperr.New(
		apperr.CodeInvalidInput,
		"no entry for "+strings.Join(missing, ", "),
	)
	if err != nil {
		return apperr.Wrap(apperr.Join(err, miss), apperr.CodeInvalidInput, "decoration")
	}
	return apperr.Wrap(miss, apperr.CodeInvalidInput, "decoration")
}

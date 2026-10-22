package toolchain

import (
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Catalog is the projected, in-memory set of lifecycle tool specs, grouped by category.
// It is the lifecycle counterpart to the [Registry]: the composition root builds
// one Catalog from the manifest's tools section and threads it into the detect/install
// operations, so those operations take their tool data as input rather than reaching for
// a package global. Registry-only manifest entries (no category) are not part of the
// catalog — they exist solely for RequireTool via DefsFromTools.
type Catalog struct {
	// prerequisites are tools that must pre-exist (checked, never installed).
	prerequisites []types.ToolDef

	// required is the build/lint/test toolchain (auto-installed, in manifest order).
	required []types.ToolDef

	// optional are developer-convenience tools (installed on demand).
	optional []types.ToolDef
}

// NewCatalog projects the lifecycle tools of the manifest's tools section into a Catalog,
// split by category and preserving manifest order within each (install ordering matters,
// e.g. Node before pnpm). A manifest ValidateToolEntries rejects (a duplicate binary, an
// unknown install kind, an unparseable min_version, …) is a CodeInvalidInput error.
func NewCatalog(tools []ToolEntry) (*Catalog, error) {
	if errs := ValidateToolEntries(tools); len(errs) > 0 {
		return nil, apperr.Wrap(apperr.Join(errs...), apperr.CodeInvalidInput,
			"invalid tool manifest")
	}
	pre, err := ProjectToolsByCategory(tools, CategoryPrerequisite)
	if err != nil {
		return nil, err
	}
	req, err := ProjectToolsByCategory(tools, CategoryRequired)
	if err != nil {
		return nil, err
	}
	opt, err := ProjectToolsByCategory(tools, CategoryOptional)
	if err != nil {
		return nil, err
	}
	return &Catalog{prerequisites: pre, required: req, optional: opt}, nil
}

// Prerequisites returns the tools that must pre-exist (checked, never installed).
func (c *Catalog) Prerequisites() []types.ToolDef { return c.prerequisites }

// Required returns the build/lint/test toolchain (auto-installed, in order).
func (c *Catalog) Required() []types.ToolDef { return c.required }

// Optional returns the developer-convenience tools (installed on demand).
func (c *Catalog) Optional() []types.ToolDef { return c.optional }

// Installable returns the tools an install-by-name command can target: the
// required toolchain, then the optional tools (prerequisites are never installed).
func (c *Catalog) Installable() []types.ToolDef {
	out := make([]types.ToolDef, 0, len(c.required)+len(c.optional))
	out = append(out, c.required...)
	return append(out, c.optional...)
}

// CI returns the installable tools the manifest marks `ci: true`, in manifest
// order within each category — the set a CI install provisions.
func (c *Catalog) CI() []types.ToolDef {
	installable := c.Installable()
	var out []types.ToolDef
	for i := range installable {
		if installable[i].CI {
			out = append(out, installable[i])
		}
	}
	return out
}

// All returns every lifecycle spec in setup order: prerequisites, then required,
// then optional.
func (c *Catalog) All() []types.ToolDef {
	all := make(
		[]types.ToolDef,
		0,
		len(c.prerequisites)+len(c.required)+len(c.optional),
	)
	all = append(all, c.prerequisites...)
	all = append(all, c.required...)
	all = append(all, c.optional...)
	return all
}

// Find returns the lifecycle spec for a binary name, searching all categories.
// The boolean is false for registry-only or unknown binaries.
func (c *Catalog) Find(binary string) (types.ToolDef, bool) {
	for _, list := range [][]types.ToolDef{c.prerequisites, c.required, c.optional} {
		for i := range list {
			if list[i].Binary == binary {
				return list[i], true
			}
		}
	}
	return types.ToolDef{}, false
}

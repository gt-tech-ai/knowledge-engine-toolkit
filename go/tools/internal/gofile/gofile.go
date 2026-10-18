// Package gofile is the generators' shared Go source writer: it names the packages a
// generated file refers to, choosing an import alias per path, and writes the import
// block for them.
package gofile

import (
	"bytes"
	"fmt"
	"go/types"
	pathpkg "path"
	"slices"
)

// Imports tracks the packages a generated file refers to.
type Imports struct {
	// aliases maps an imported package path to the name the file refers to it by.
	aliases map[string]string

	// names maps an imported package path to its package name.
	names map[string]string

	// self is the generated file's import path; its types are written unqualified.
	self string
}

// NewImports returns the Imports of a file whose import path is self.
func NewImports(self string) *Imports {
	return &Imports{self: self, aliases: map[string]string{}, names: map[string]string{}}
}

// Qualify is the types.Qualifier that names pkg by its import alias.
func (g *Imports) Qualify(pkg *types.Package) string {
	if pkg.Path() == g.self {
		return ""
	}
	return g.Alias(pkg.Path(), pkg.Name())
}

// Alias returns the name path is imported as, choosing name, or name with a numeric
// suffix when another path already took it.
func (g *Imports) Alias(path, name string) string {
	if a, ok := g.aliases[path]; ok {
		return a
	}
	a := name
	for i := 2; slices.Contains(g.taken(), a); i++ {
		a = fmt.Sprintf("%s%d", name, i)
	}
	g.aliases[path] = a
	g.names[path] = name
	return a
}

// Type writes t qualified by the file's imports.
func (g *Imports) Type(t types.Type) string { return types.TypeString(t, g.Qualify) }

// Taken reports whether name is the alias of an imported package, so a generated
// identifier must not reuse it.
func (g *Imports) Taken(name string) bool { return slices.Contains(g.taken(), name) }

// taken lists the aliases in use.
func (g *Imports) taken() []string {
	out := make([]string, 0, len(g.aliases))
	for _, a := range g.aliases {
		out = append(out, a)
	}
	return out
}

// Write writes the import block.
func (g *Imports) Write(out *bytes.Buffer) {
	paths := make([]string, 0, len(g.aliases))
	for p := range g.aliases {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	out.WriteString("import (\n")
	for _, p := range paths {
		if a := g.aliases[p]; a != g.names[p] || a != pathpkg.Base(p) {
			fmt.Fprintf(out, "\t%s %q\n", a, p)
			continue
		}
		fmt.Fprintf(out, "\t%q\n", p)
	}
	out.WriteString(")\n\n")
}

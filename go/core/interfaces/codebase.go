package interfaces

import (
	"context"
	"regexp"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Codebase is a read-only view of one codebase (a git working tree, a plain directory
// or an in-memory file set) for the life of one run: its files, their contents, pattern
// matches over them, the Go and YAML parsed forms and rendered charts. Every view is
// computed once per run and shared by concurrent callers, so a check that reads a file
// another check already parsed costs no second parse.
type Codebase interface {
	// Files returns the files sel selects, sorted by path.
	Files(ctx context.Context, sel types.FileSelector) ([]types.File, error)

	// Text returns one file's contents. A path outside the repository is
	// CodeInvalidInput; a missing file is CodeNotFound.
	Text(ctx context.Context, path string) (types.Text, error)

	// Match returns, for every non-binary file sel selects, each line that matches one
	// of patterns (the first matching pattern per line), sorted by file and line.
	Match(
		ctx context.Context,
		sel types.FileSelector,
		patterns []*regexp.Regexp,
	) ([]types.Hit, error)

	// Changed returns the paths that differ from base (a commit-ish) plus the untracked
	// files. A source without version control returns an empty set; an unknown base is
	// CodeNotFound.
	Changed(ctx context.Context, base string) (map[string]bool, error)

	// Go returns the repository's Go view.
	Go() GoView

	// YAML returns the repository's YAML view.
	YAML() YAMLView

	// Rendered returns the objects spec renders. A repository built without a renderer
	// returns CodeUnavailable.
	Rendered(ctx context.Context, spec types.RenderSpec) ([]types.Object, error)
}

// GoView reads a repository's Go modules and parsed Go files.
type GoView interface {
	// Modules returns the repository-relative directories of the Go modules: the `use`
	// entries of go.work when it exists, else the directory of every go.mod.
	Modules(ctx context.Context) ([]string, error)

	// Parse parses every Go file sel selects in mode, sorted by path. A file that does
	// not parse is CodeInvalidInput naming it.
	Parse(
		ctx context.Context,
		sel types.FileSelector,
		mode types.ParseMode,
	) ([]types.GoFile, error)
}

// YAMLView reads a repository's YAML documents.
type YAMLView interface {
	// Docs returns every document of every YAML file sel selects, in path then document
	// order. A file that does not parse is CodeInvalidInput naming it.
	Docs(ctx context.Context, sel types.FileSelector) ([]types.YAMLDoc, error)

	// Query returns the nodes path selects in doc. The path language is `$` (the
	// document), `.key`, `..key` (at any depth), `.*`, `[n]` and `[*]`.
	Query(ctx context.Context, doc types.YAMLDoc, path string) ([]types.YAMLMatch, error)

	// Decode decodes doc into out, as yaml.Unmarshal would.
	Decode(ctx context.Context, doc types.YAMLDoc, out any) error
}

// CodebaseSource is where a Codebase's files come from: a version-controlled working
// tree, a plain directory, or an in-memory map. Codebase builds every view on top of it.
type CodebaseSource interface {
	// List returns every file of the repository, sorted by path.
	List(ctx context.Context) ([]types.File, error)

	// Read returns one file's contents. A path outside the repository is
	// CodeInvalidInput; a missing file is CodeNotFound.
	Read(ctx context.Context, path string) ([]byte, error)

	// Changed returns the paths that differ from base plus the untracked files; a source
	// without version control returns an empty set.
	Changed(ctx context.Context, base string) (map[string]bool, error)
}

// Renderer renders a chart to its manifest objects.
type Renderer interface {
	// Render renders spec and decodes its documents, in output order.
	Render(ctx context.Context, spec types.RenderSpec) ([]types.Object, error)
}

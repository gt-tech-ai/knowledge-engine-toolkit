// Package toolchain owns a developer tool chain's lifecycle, projected from one list of
// [types.ToolEntry] values (a tool manifest the consumer loads and checks with
// [types.ValidateToolEntries]; the manifest's schema lives in core/types so a
// foundation-tier manifest can hold it). Its files:
//
//   - tool.go / registry.go — [Registry], a lookup of lightweight [Tool] values (binary +
//     optional command prefix) for RequireTool / prefix expansion (e.g. `uv run ruff`).
//     The composition root builds one from the manifest's tools via [DefsFromTools] +
//     [NewRegistryFromDefs] and injects it; there is no package-level registry.
//   - specs.go — projects the manifest's lifecycle tools (those carrying a `category`)
//     into heavy [types.ToolDef] specs, resolving the declarative version-parse
//     strategy and per-platform install kinds.
//   - catalog.go — [Catalog], the lifecycle counterpart to the registry: the composition
//     root builds one from the manifest's tools and threads it into the detect/install
//     operations (Prerequisites/Required/Optional).
//   - detect.go — [Detector] checks tools for presence + minimum version through an
//     injected CommandRunner (so detection is testable and host-independent).
//   - install.go / path.go — [Installer] resolves and runs the platform-appropriate
//     install method, manages PATH, and orchestrates the setup-tool flow
//     ([Installer.Provision]) and the tool-install flow
//     ([Installer.InstallTools]: the CI set, named tools, or the optional tools).
//   - download.go — the pinned download kinds (github-release, url): fetch the host's
//     os-arch artifact through the [Downloader] seam, refuse it unless its SHA-256
//     matches the manifest, and atomically place the (extracted) binary in GOBIN.
//   - pins.go — [Installer.CheckPins] compares each go-installed CI tool on PATH with
//     the build the manifest pins.
//
// The two tool representations (lightweight [Tool], heavy [types.ToolDef]) coexist,
// both projected from the same manifest list but serving different consumers.
package toolchain

import (
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Registry holds [types.Tool] instances keyed by binary name.
type Registry struct {
	// tools maps binary name to its Tool descriptor.
	tools map[string]*Tool

	// mu guards concurrent reads and writes to tools.
	mu sync.RWMutex
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

// Register adds a tool to the registry, keyed by binary name.
func (r *Registry) Register(t *Tool) {
	r.mu.Lock()
	r.tools[t.Binary()] = t
	r.mu.Unlock()
}

// Get returns the tool with the given binary name.
// Returns a minimal fallback Tool if the binary is not registered.
func (r *Registry) Get(binary string) *Tool {
	r.mu.RLock()
	t, ok := r.tools[binary]
	r.mu.RUnlock()
	if ok {
		return t
	}
	return NewTool(binary, binary, "", nil)
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// NewRegistryFromDefs builds a Registry holding one [types.Tool] per Def. A
// later Def with the same binary replaces an earlier one.
func NewRegistryFromDefs(defs []Def) *Registry {
	r := NewRegistry()
	for _, d := range defs {
		r.Register(NewTool(d.Name, d.Binary, d.InstallHint, d.Prefix))
	}
	return r
}

// Def describes a tool for registry construction, consumed by
// [NewRegistryFromDefs] and produced from the manifest's tools by [DefsFromTools].
type Def struct {
	// Name is the human-readable display name (e.g. "golangci-lint").
	Name string

	// Binary is the executable name looked up on PATH.
	Binary string

	// InstallHint is shown when the tool is missing from PATH.
	InstallHint string

	// Prefix is an optional command prefix (e.g. ["uv", "run"] for Python tools).
	Prefix []string
}

// DefsFromTools projects the manifest's tools into registry Defs: each tool's name,
// binary, install hint and command prefix.
func DefsFromTools(tools []types.ToolEntry) []Def {
	defs := make([]Def, 0, len(tools))
	for i := range tools {
		t := &tools[i]
		defs = append(defs, Def{
			Name:        t.Name,
			Binary:      t.Binary,
			InstallHint: t.InstallHint,
			Prefix:      t.Cmd,
		})
	}
	return defs
}

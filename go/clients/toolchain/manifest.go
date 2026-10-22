package toolchain

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// InstallKind discriminates how a tool is installed on a given platform. The
// per-platform parameters live in the manifest; the install mechanism that
// turns them into a command lives in toolchain.
type InstallKind string

const (
	// InstallKindCommand runs a literal command with args (brew/apt/winget/sh/...).
	InstallKindCommand InstallKind = "command"
	// InstallKindGoInstall runs `go install <package>`.
	InstallKindGoInstall InstallKind = "go-install"
	// InstallKindGitHubRelease downloads a pinned, checksum-verified prebuilt
	// binary from a GitHub release (org/repo/tag + per-platform assets).
	InstallKindGitHubRelease InstallKind = "github-release"
	// InstallKindURL downloads a pinned, checksum-verified prebuilt binary from a
	// release host (the per-platform asset name is appended to URL).
	InstallKindURL InstallKind = "url"
)

// ToolCategory classifies a tool's role in the developer-environment lifecycle.
// An entry with no category is registry-only (used by RequireTool / prefix
// expansion) and is not managed by setup and tool installs.
type ToolCategory string

const (
	// CategoryPrerequisite must exist before setup; checked, never auto-installed
	// (the user is pointed at fallback_url). E.g. Go, Docker.
	CategoryPrerequisite ToolCategory = "prerequisite"
	// CategoryRequired is part of the build/lint/test toolchain; auto-installed.
	CategoryRequired ToolCategory = "required"
	// CategoryOptional is a developer convenience tool, installed on demand via
	// the install command.
	CategoryOptional ToolCategory = "optional"
)

// Version-parse strategy names. The strategy is declarative in the manifest;
// toolchain resolves it to the actual parser (a Go func cannot be YAML).
const (
	VersionParseFirst = "first-version" // default: first digits.digits run
	VersionParseGo    = "go-version"    // "go version go1.26.0 ..." -> "1.26.0"
)

// InstallMethodEntry is a per-platform install method, discriminated by Kind.
// Only the fields relevant to Kind are populated.
type InstallMethodEntry struct {
	// Assets maps an os-arch platform (linux-amd64, linux-arm64, darwin-amd64,
	// darwin-arm64, windows-amd64, windows-arm64) to the release asset and its
	// SHA-256, for the download kinds (InstallKindGitHubRelease, InstallKindURL).
	// A host whose os-arch has no entry cannot install the tool this way.
	Assets map[string]AssetEntry `yaml:"assets,omitempty"`

	// Kind selects which mechanism (and therefore which fields) apply.
	Kind InstallKind `yaml:"kind"`

	// Command is the executable to run for InstallKindCommand (e.g. "brew").
	Command string `yaml:"command,omitempty"`

	// Package is the module path for InstallKindGoInstall (`go install <Package>`);
	// its `@vX.Y.Z` suffix is the pinned version.
	Package string `yaml:"package,omitempty"`

	// Org is the GitHub owner for InstallKindGitHubRelease.
	Org string `yaml:"org,omitempty"`

	// Repo is the GitHub repository for InstallKindGitHubRelease.
	Repo string `yaml:"repo,omitempty"`

	// Tag is the release tag for InstallKindGitHubRelease (e.g. "v1.69.0").
	Tag string `yaml:"tag,omitempty"`

	// Version is the pinned tool version the method installs (e.g. "v2.15.1").
	// Required for the download kinds; on InstallKindCommand it records the
	// version its args pin. InstallKindGoInstall pins it in Package instead.
	Version string `yaml:"version,omitempty"`

	// URL is the HTTPS base URL for InstallKindURL; each asset's name is appended
	// after a "/" to form its download URL.
	URL string `yaml:"url,omitempty"`

	// Binary is the installed executable's base name for the download kinds.
	Binary string `yaml:"binary,omitempty"`

	// Args are the arguments for InstallKindCommand (e.g. ["install", "uv"]).
	Args []string `yaml:"args,omitempty"`
}

// AssetEntry is one platform's release asset for a download kind.
type AssetEntry struct {
	// Name is the asset's file name: the GitHub release asset
	// (InstallKindGitHubRelease) or the path appended to URL (InstallKindURL). A
	// .tar.gz / .tgz / .zip name is an archive the binary is extracted from.
	Name string `yaml:"name"`

	// SHA256 is the lowercase hex SHA-256 of the asset, taken from the release's
	// published checksums; the installer refuses any download that differs.
	SHA256 string `yaml:"sha256"`
}

// sha256Hex matches a lowercase hex SHA-256 digest.
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// assetPlatforms reports whether platform is an os-arch key an AssetEntry may be declared
// for.
func assetPlatforms(platform string) bool {
	switch platform {
	case "linux-amd64", "linux-arm64",
		"darwin-amd64", "darwin-arm64",
		"windows-amd64", "windows-arm64":
		return true
	default:
		return false
	}
}

// validate reports a structural error for a single install method: the fields its
// Kind requires, an https URL for the url kind, and the download checks
// (validateDownload) for both download kinds.
func (m InstallMethodEntry) validate() error {
	// Each kind's required fields, and the message naming them when one is empty.
	required := map[InstallKind]struct {
		msg    string
		fields []string
	}{
		InstallKindCommand: {
			"command install kind requires a command",
			[]string{m.Command},
		},
		InstallKindGoInstall: {
			"go-install kind requires a package",
			[]string{m.Package},
		},
		InstallKindGitHubRelease: {
			"github-release kind requires org, repo, tag",
			[]string{m.Org, m.Repo, m.Tag},
		},
		InstallKindURL: {"url kind requires an https:// url", []string{m.URL}},
	}
	req, known := required[m.Kind]
	switch {
	case !known:
		return apperr.New(apperr.CodeInvalidInput,
			fmt.Sprintf("unknown install kind %q", m.Kind))
	case slices.Contains(req.fields, ""):
		return apperr.New(apperr.CodeInvalidInput, req.msg)
	case m.Kind == InstallKindURL && !strings.HasPrefix(m.URL, "https://"):
		return apperr.New(apperr.CodeInvalidInput, "url kind requires an https:// url")
	case m.Kind == InstallKindGitHubRelease || m.Kind == InstallKindURL:
		return m.validateDownload()
	}
	return nil
}

// archiveSuffixes are the asset suffixes the installer extracts from — archive
// containers (.tar.gz/.tgz/.zip) and a single-file gzip (.gz, decompressed
// straight to the binary); unpackedSuffixes are archive or compression suffixes
// it can't extract, which would otherwise be installed as if they were the raw
// binary. (.tar.gz is checked before .gz so a tarball never matches the .gz path.)
func archiveSuffixes() []string { return []string{".tar.gz", ".tgz", ".zip", ".gz"} }

func unpackedSuffixes() []string {
	return []string{".tar", ".xz", ".bz2", ".zst", ".7z", ".txz"}
}

// validateDownload checks the fields both download kinds share: a pinned
// version, the binary name (one plain file name, so it can't escape the install
// dir), and at least one asset, each for a known os-arch, carrying a well-formed
// SHA-256 — so a download can never be unverified — and named either as an archive
// the installer extracts or as the raw binary.
func (m InstallMethodEntry) validateDownload() error {
	if m.Version == "" || m.Binary == "" || len(m.Assets) == 0 {
		return apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("%s kind requires version, binary, and assets", m.Kind),
		)
	}
	if !filepath.IsLocal(m.Binary) || strings.ContainsAny(m.Binary, `/\`) {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"binary %q must be a plain file name", m.Binary,
		))
	}
	for platform, a := range m.Assets {
		if !assetPlatforms(platform) {
			return apperr.New(
				apperr.CodeInvalidInput,
				fmt.Sprintf("asset platform %q is not an os-arch key", platform),
			)
		}
		if a.Name == "" || !sha256Hex.MatchString(a.SHA256) {
			return apperr.New(
				apperr.CodeInvalidInput,
				fmt.Sprintf("asset %q needs a name and a 64-hex sha256", platform),
			)
		}
		if unsupportedArchive(a.Name) {
			return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"asset %q (%s) is an archive format the installer can't extract (%s)",
				platform, a.Name, strings.Join(archiveSuffixes(), ", "),
			))
		}
	}
	return nil
}

// unsupportedArchive reports whether name ends in an archive or compression suffix
// other than the ones the installer extracts.
func unsupportedArchive(name string) bool {
	has := func(sfx string) bool { return strings.HasSuffix(name, sfx) }
	return !slices.ContainsFunc(archiveSuffixes(), has) &&
		slices.ContainsFunc(unpackedSuffixes(), has)
}

// ToolEntry is the single source of truth for one tool in a tool manifest. The
// lightweight fields (Name/Binary/InstallHint/Cmd) feed the toolchain.Registry via
// DefsFromTools for RequireTool; the lifecycle fields (Category, version
// detection, per-platform Install) are projected into a types.ToolDef by
// toolchain for setup and tool installs. Registry-only tools omit
// the lifecycle fields.
type ToolEntry struct {
	// Install maps a platform key (darwin/windows/linux-apt/linux-dnf/
	// linux-pacman/all) to its install method. Empty for registry-only and
	// prerequisite tools.
	Install map[string]InstallMethodEntry `yaml:"install,omitempty"`

	// Name is the human-readable display name (e.g. "golangci-lint").
	Name string `yaml:"name"`

	// Binary is the executable name looked up on PATH.
	Binary string `yaml:"binary"`

	// InstallHint is the short manual-install hint shown by RequireTool.
	InstallHint string `yaml:"install_hint,omitempty"`

	// Category classifies the tool's lifecycle role; empty means registry-only.
	Category ToolCategory `yaml:"category,omitempty"`

	// Description is a one-line summary shown by a tool listing.
	Description string `yaml:"description,omitempty"`

	// MinVersion is the minimum acceptable version as "major.minor" (optional).
	MinVersion string `yaml:"min_version,omitempty"`

	// VersionParse names the strategy used to extract a version from VersionCmd
	// output (empty/"first-version" or "go-version").
	VersionParse string `yaml:"version_parse,omitempty"`

	// FallbackURL is the manual-install URL shown when the tool can't be installed.
	FallbackURL string `yaml:"fallback_url,omitempty"`

	// Cmd is an optional command prefix for registry tools (e.g. ["uv","run"]).
	Cmd []string `yaml:"cmd,omitempty"`

	// VersionCmd is the command that prints the tool's version (e.g. ["go","version"]).
	VersionCmd []string `yaml:"version_cmd,omitempty"`

	// Required marks a tool whose absence should block (prerequisites + toolchain).
	Required bool `yaml:"required,omitempty"`

	// CI marks a tool CI provisions: a CI install installs exactly
	// the ci-marked set. Only an installable lifecycle tool (required/optional)
	// may carry it.
	CI bool `yaml:"ci,omitempty"`
}

// InLifecycle reports whether this tool is managed by the setup/tools lifecycle
// (i.e. carries a category), as opposed to being registry-only.
func (t ToolEntry) InLifecycle() bool { return t.Category != "" }

// validate reports all structural errors for a single tool entry.
func (t ToolEntry) validate() []error {
	var errs []error
	if t.Binary == "" {
		errs = append(
			errs,
			apperr.New(
				apperr.CodeInvalidInput,
				fmt.Sprintf("tool %q has empty binary", t.Name),
			),
		)
	}
	if !validVersionParse(t.VersionParse) {
		errs = append(
			errs,
			apperr.New(
				apperr.CodeInvalidInput,
				fmt.Sprintf(
					"tool %q has unknown version_parse %q",
					t.Binary,
					t.VersionParse,
				),
			),
		)
	}
	if t.CI && t.Category != CategoryRequired && t.Category != CategoryOptional {
		errs = append(
			errs,
			apperr.New(
				apperr.CodeInvalidInput,
				fmt.Sprintf("tool %q is ci but not a required/optional tool", t.Binary),
			),
		)
	}
	if t.Category != "" && !validCategory(t.Category) {
		errs = append(
			errs,
			apperr.New(
				apperr.CodeInvalidInput,
				fmt.Sprintf("tool %q has unknown category %q", t.Binary, t.Category),
			),
		)
	}
	for platform := range t.Install {
		method := t.Install[platform]
		if err := method.validate(); err != nil {
			errs = append(
				errs,
				apperr.Wrap(
					err,
					apperr.CodeInvalidInput,
					fmt.Sprintf("tool %q platform %q", t.Binary, platform),
				),
			)
		}
	}
	return errs
}

// validVersionParse reports whether s is a known parse strategy ("" = default).
func validVersionParse(s string) bool {
	switch s {
	case "", VersionParseFirst, VersionParseGo:
		return true
	default:
		return false
	}
}

// validCategory reports whether c is a known tool category.
func validCategory(c ToolCategory) bool {
	switch c {
	case CategoryPrerequisite, CategoryRequired, CategoryOptional:
		return true
	default:
		return false
	}
}

// ValidateToolEntries collects validation errors across the tool list, including a
// binary declared by two entries (a lookup by binary would silently pick one).
func ValidateToolEntries(tools []ToolEntry) []error {
	var errs []error
	seen := make(map[string]bool, len(tools))
	for i := range tools {
		errs = append(errs, tools[i].validate()...)
		if b := tools[i].Binary; b != "" {
			if seen[b] {
				errs = append(errs, apperr.New(apperr.CodeInvalidInput,
					fmt.Sprintf("tool %q: duplicate binary %q", tools[i].Name, b)))
			}
			seen[b] = true
		}
	}
	return errs
}

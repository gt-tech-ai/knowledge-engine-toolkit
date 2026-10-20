package types

import (
	"fmt"
	"strconv"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// ---------------------------------------------------------------------------
// Tool lifecycle model (ToolDef): detection + installation
// ---------------------------------------------------------------------------

// Version represents a semantic version with major and minor components.
type Version struct {
	// Major is the major version component.
	Major int

	// Minor is the minor version component.
	Minor int
}

// String returns the version as "Major.Minor".
func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

// MeetsMinimum returns true if v >= required.
func (v Version) MeetsMinimum(required Version) bool {
	if v.Major != required.Major {
		return v.Major > required.Major
	}
	return v.Minor >= required.Minor
}

// InstallMethod describes how to install a tool on a given platform: either a
// command to run, or (when Download is set) a pinned artifact the installer
// fetches, verifies against its SHA-256, and places itself.
type InstallMethod struct {
	// Download, when non-nil, installs a pinned, checksum-verified artifact (the
	// github-release and url kinds) instead of running Command.
	Download *Download

	// Command is the executable to run (e.g. "brew", "go", "powershell").
	Command string

	// Args are the arguments passed to Command.
	Args []string
}

// Download is a pinned binary release: one artifact per os-arch platform, each
// with the SHA-256 it must match before its binary is placed on PATH.
type Download struct {
	// Artifacts maps an os-arch platform ("linux-amd64", "darwin-arm64",
	// "windows-amd64", ...) to that platform's artifact.
	Artifacts map[string]Artifact

	// Binary is the installed executable's base name (".exe" is added on Windows).
	Binary string

	// Version is the pinned tool version the artifacts carry (for reporting).
	Version string
}

// Artifact is one platform's downloadable file: a raw binary, or a .tar.gz /
// .tgz / .zip archive containing it.
type Artifact struct {
	// URL is the absolute HTTPS download URL.
	URL string

	// SHA256 is the lowercase hex SHA-256 the downloaded bytes must match.
	SHA256 string
}

// ToolStatus describes the result of checking a single tool.
type ToolStatus int

const (
	StatusToolOK       ToolStatus = iota // tool present, version OK
	StatusToolOutdated                   // tool present but version too low
	StatusToolMissing                    // tool not found on PATH
)

// ToolCheckResult is returned when checking a tool.
type ToolCheckResult struct {
	// Tool is the spec that was checked.
	Tool *ToolDef

	// Found is the detected version (or "installed"/"unknown"), for reporting.
	Found string

	// Message is the human-readable status line.
	Message string

	// Status classifies the outcome (OK / Outdated / Missing).
	Status ToolStatus
}

// ToolDef defines a development tool, how to detect it, and how to install it.
// It is the heavy lifecycle counterpart to Tool (see the package doc).
type ToolDef struct {
	// MinVersion is the minimum acceptable version; nil skips the version check.
	MinVersion *Version

	// VersionParse extracts a version string from VersionCmd's raw output.
	VersionParse func(string) string

	// InstallMethods maps a platform key to its install method.
	InstallMethods map[string]InstallMethod

	// Name is the human-readable display name.
	Name string

	// Binary is the executable name looked up on PATH.
	Binary string

	// Description is a one-line summary shown by a tool listing.
	Description string

	// FallbackURL is the manual-install URL shown when the tool can't be installed.
	FallbackURL string

	// VersionCmd is the command that prints the tool's version.
	VersionCmd []string

	// Required marks a tool whose absence should block.
	Required bool

	// CI marks a tool CI provisions (a CI install).
	CI bool
}

// InstallToolsOptions configures the tool installation orchestration.
type InstallToolsOptions struct {
	// Names filters installation to specific tool names. If empty, all missing
	// tools are considered.
	Names []string

	// AutoYes skips the confirmation prompt when true.
	AutoYes bool

	// Force reinstalls every targeted tool even when it is already present at a
	// satisfying version — the rebuild path after a Go-toolchain bump, where the
	// go-install binaries are the right version but built with the old Go.
	Force bool

	// CI targets exactly the tools the manifest marks `ci: true` (required and
	// optional alike); it cannot be combined with Names.
	CI bool
}

// ParseFirstVersion extracts a version-like substring (digits.digits...) from s.
func ParseFirstVersion(s string) string {
	var start int
	found := false
	for i, c := range s {
		if c >= '0' && c <= '9' {
			if !found {
				start = i
				found = true
			}
		} else if found && c != '.' {
			return s[start:i]
		}
	}
	if found {
		return s[start:]
	}
	return ""
}

// ParseVersion parses a version string like "3.12.4" or "v1.26" into a Version.
func ParseVersion(s string) (Version, error) {
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimSpace(s)
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return Version{}, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("invalid version %q: expected major.minor", s),
		)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return Version{}, apperr.Wrap(err, apperr.CodeInvalidInput,
			fmt.Sprintf("invalid major version %q", parts[0]))
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return Version{}, apperr.Wrap(err, apperr.CodeInvalidInput,
			fmt.Sprintf("invalid minor version %q", parts[1]))
	}
	return Version{Major: major, Minor: minor}, nil
}

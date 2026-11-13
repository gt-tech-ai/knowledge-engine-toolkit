package types

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// maxVersionParts is the dotted components of a version: major, minor, patch.
const maxVersionParts = 3

// minVersionParts is the fewest dotted components a version may have.
const minVersionParts = 2

// ---------------------------------------------------------------------------
// Tool lifecycle model (ToolDef): detection + installation
// ---------------------------------------------------------------------------

// Version represents a semantic version: major, minor and an optional patch.
type Version struct {
	// Major is the major version component.
	Major int

	// Minor is the minor version component.
	Minor int

	// Patch is the patch component; 0 when the version names none.
	Patch int
}

// String returns the version as "Major.Minor", or "Major.Minor.Patch" when Patch
// is non-zero.
func (v Version) String() string {
	if v.Patch == 0 {
		return fmt.Sprintf("%d.%d", v.Major, v.Minor)
	}

	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// MeetsMinimum returns true if v >= required, comparing major, then minor, then
// patch, so a toolchain one patch behind a patch-level minimum fails it.
func (v Version) MeetsMinimum(required Version) bool {
	if v.Major != required.Major {
		return v.Major > required.Major
	}

	if v.Minor != required.Minor {
		return v.Minor > required.Minor
	}

	return v.Patch >= required.Patch
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

	// Member is the archive entry holding the binary, for a release that names it per
	// platform (helm-unittest's untt-linux-amd64); empty means the binary's own name.
	Member string
}

// ToolStatus describes the result of checking a single tool.
type ToolStatus int

const (
	// StatusToolOK means the tool is present and its version is acceptable.
	StatusToolOK ToolStatus = iota
	// StatusToolOutdated means the tool is present but its version is too low.
	StatusToolOutdated
	// StatusToolMissing means the tool is not on PATH.
	StatusToolMissing
)

// ParseFirstVersion extracts a version-like substring (digits.digits...) from s.
func ParseFirstVersion(text string) string {
	start := strings.IndexFunc(text, isVersionDigit)
	if start < 0 {
		return ""
	}

	length := strings.IndexFunc(text[start:], func(c rune) bool {
		return !isVersionDigit(c) && c != '.'
	})
	if length < 0 {
		return text[start:]
	}

	return text[start : start+length]
}

// isVersionDigit reports whether c is an ASCII digit.
func isVersionDigit(c rune) bool {
	return c >= '0' && c <= '9'
}

// ParseVersion parses a version string like "3.12.4" or "v1.26" into a Version.
// The patch keeps only its leading digits ("4-beta" reads as 4); a missing or
// non-numeric patch reads as 0.
func ParseVersion(text string) (Version, error) {
	text = strings.TrimPrefix(text, "v")
	text = strings.TrimSpace(text)

	parts := strings.SplitN(text, ".", maxVersionParts)

	if len(parts) < minVersionParts {
		return Version{}, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("invalid version %q: expected major.minor", text),
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

	return Version{Major: major, Minor: minor, Patch: parsePatch(parts)}, nil
}

// parsePatch returns the leading digits of the third dotted part as the patch,
// or 0 when there is no third part or it starts with a non-digit.
func parsePatch(parts []string) int {
	if len(parts) < maxVersionParts {
		return 0
	}

	digits := parts[maxVersionParts-1]

	end := strings.IndexFunc(digits, func(c rune) bool { return !isVersionDigit(c) })
	if end >= 0 {
		digits = digits[:end]
	}

	patch, err := strconv.Atoi(digits)
	if err != nil {
		return 0
	}

	return patch
}

package types

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
	// CheckOnly reports what is missing and installs nothing.
	CheckOnly bool

	// Force reinstalls every targeted tool even when it is already present at a
	// satisfying version — the rebuild path after a Go-toolchain bump, where the
	// go-install binaries are the right version but built with the old Go.
	Force bool

	// CI targets exactly the tools the manifest marks `ci: true` (required and
	// optional alike); it cannot be combined with Names.
	CI bool

	// DryRun lists the tools the install would put in place and installs nothing.
	DryRun bool
}

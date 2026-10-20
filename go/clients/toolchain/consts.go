package toolchain

// Host OS/arch identifiers (as reported by runtime.GOOS/GOARCH) and the shared tool
// subcommand verb used when assembling runner argument slices.
const (
	osWindows = "windows"
	osDarwin  = "darwin"
	archARM64 = "arm64"
	cmdRun    = "run"
)

package toolchain

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// PlatformKey returns the platform key used to select install methods:
// "darwin", "windows", or "linux-apt"/"linux-dnf"/"linux-pacman".
func PlatformKey() string {
	switch runtime.GOOS {
	case osDarwin:
		return osDarwin
	case osWindows:
		return osWindows
	case "linux":
		return detectLinuxPackageManager()
	default:
		return runtime.GOOS
	}
}

// detectLinuxPackageManager reads /etc/os-release to pick the package manager,
// defaulting to apt when the distro can't be determined.
func detectLinuxPackageManager() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "linux-apt"
	}
	content := strings.ToLower(string(data))
	switch {
	case strings.Contains(content, "arch"):
		return "linux-pacman"
	case strings.Contains(content, "fedora"), strings.Contains(content, "rhel"),
		strings.Contains(content, "centos"), strings.Contains(content, "rocky"):
		return "linux-dnf"
	default:
		return "linux-apt"
	}
}

// Installer installs tools using platform-appropriate methods, routing every
// command through a CommandRunner so the exact invocation is testable and a
// forced platform exercises platform-specific branches without a real install.
// The platform key is injected (not detected) so callers — and tests — control it.
type Installer struct {
	// runner executes every install (and post-install verify) command.
	runner interfaces.CommandRunner

	// prompter asks the operator to confirm an install (caller-injected: a CLI
	// passes a tty Prompter, a front-end with no operator the deny Prompter; nil
	// fails an install that needs confirming with a coded error).
	prompter interfaces.Prompter

	// downloader fetches the artifacts of the pinned download install kinds
	// (default: HTTPS GET; overridden by WithDownloader).
	downloader Downloader

	// paths finds a tool's binary on PATH for the pin check (default:
	// os/exec.LookPath; overridden by WithPathResolver).
	paths PathResolver

	// log receives install progress and failure output.
	log interfaces.Logger

	// reinstallHint is the command a pin drift tells the operator to run for a tool
	// (default: `go install <package>@<version>`; overridden by WithReinstallHint).
	reinstallHint func(binary, pkgAtVersion string) string

	// platform is the injected platform key (darwin/windows/linux-apt/...) used
	// to select install methods.
	platform string

	// arch is the host architecture, for the Windows-ARM64 cross-compile rewrite.
	arch string

	// force reinstalls required tools via Provision even when already present at a
	// satisfying version — the rebuild path after a Go-toolchain bump (set by
	// WithForce; the optional-tool flow reads InstallToolsOptions.Force instead).
	force bool
}

// Option configures an Installer at construction (charter §7: dependencies are
// injected, not detected in a constructor).
type Option func(*Installer)

// WithArch overrides the host architecture used for the Windows-ARM64
// cross-compile rewrite. The default is runtime.GOARCH; injecting it makes the
// arch-specific rewrite deterministically testable without a matching host.
func WithArch(arch string) Option {
	return func(i *Installer) { i.arch = arch }
}

// WithForce makes Provision reinstall required tools even when already present at
// a satisfying version — the a forced setup rebuild path after a Go bump.
func WithForce(force bool) Option {
	return func(i *Installer) { i.force = force }
}

// NewInstaller returns an Installer for the given platform key (see PlatformKey).
// The host architecture defaults to runtime.GOARCH (used to pick a download's
// os-arch artifact and for the Windows-ARM64 cross-compile rewrite) and can be
// overridden via WithArch; the artifact downloader defaults to HTTPS and can be
// overridden via WithDownloader; the PATH lookup defaults to os/exec.LookPath and can
// be overridden via WithPathResolver. The prompter confirms an install when AutoYes is
// off; it is required — a nil prompter fails such an install with a coded error.
func NewInstaller(
	runner interfaces.CommandRunner,
	log interfaces.Logger,
	platform string,
	prompter interfaces.Prompter,
	opts ...Option,
) *Installer {
	i := &Installer{
		runner:     runner,
		downloader: newHTTPDownloader(),
		paths:      execPathResolver{},
		log:        log,
		platform:   platform,
		arch:       runtime.GOARCH,
		prompter:   prompter,
	}
	for _, opt := range opts {
		opt(i)
	}
	return i
}

// Install installs one tool using the method resolved for the Installer's
// platform, returning a CodeUnavailable error when no method applies. A pinned
// download method is fetched, verified against its SHA-256, and placed in the
// install dir (GOBIN) without running any command; it fails, leaving nothing
// installed, on a mismatch or when the host's os-arch has no pinned checksum.
func (i *Installer) Install(
	ctx context.Context,
	root string,
	tool *types.ToolDef,
) error {
	method, ok := i.resolveMethod(tool)
	if !ok {
		return apperr.New(apperr.CodeUnavailable, fmt.Sprintf(
			"no install method available for %s on %s", tool.Name, i.platform,
		))
	}
	if method.Download != nil {
		return i.installDownload(ctx, tool.Name, method.Download)
	}

	inv := i.buildInvocation(method)
	if inv.command == "winget" {
		return i.runWinget(ctx, root, tool.Name, inv.args)
	}
	if isGoInstall(method) && (i.platform == osWindows && i.arch == archARM64) {
		i.log.Info(
			"cross-compiling as amd64 (Windows ARM64 workaround)",
			"tool",
			tool.Name,
		)
	}
	return i.runner.RunWithEnv(ctx, root, inv.env, inv.command, inv.args...)
}

// Provision runs the required-tool flow for setup: pin the local Go
// toolchain, ensure the go-install bin dir is on PATH, check prerequisites, scan
// the required toolchain, and — unless checkOnly — install the missing ones that
// have an applicable method, verifying each afterwards. A required tool with no
// install method for this platform (e.g. gcc off Windows) is reported, not
// force-installed.
func (i *Installer) Provision(
	ctx context.Context,
	root string,
	cat *Catalog,
	autoYes, checkOnly bool,
) error {
	_ = os.Setenv("GOTOOLCHAIN", "local")
	i.log.Debug("platform detected", "key", i.platform)
	i.ensureGoBinInPath()
	if i.platform == osWindows {
		i.ensureMsys2InPath()
	}
	if i.platform == osDarwin && !i.runner.Exists("brew") {
		i.log.Warn("Homebrew not found; some tools may need manual installation")
		i.log.Info("Install Homebrew: https://brew.sh")
	}

	det := NewDetector(i.runner, i.log)
	i.log.Info("Prerequisites:")
	prereqErr := det.CheckPrerequisites(ctx, root, cat.Prerequisites())
	i.log.Info("")

	i.log.Info("Tools:")
	required := cat.Required()
	results := det.Scan(ctx, root, required)
	if checkOnly {
		return SummarizeMissing(prereqErr, results)
	}

	toInstall := i.selectInstallable(required, results, i.force)
	if len(toInstall) == 0 || prereqErr != nil {
		return prereqErr
	}
	yes, err := i.confirmRequired(ctx, required, toInstall, autoYes)
	if err != nil {
		return err
	}
	if !yes {
		return reportSkipped(required, toInstall, i.log)
	}
	return i.installAndVerify(ctx, root, det, required, toInstall)
}

// selectInstallable returns the indices of required tools that need installing
// (missing or outdated, or ANY tool when force is set — the post-Go-bump rebuild)
// and have an install method applicable to this platform.
func (i *Installer) selectInstallable(
	required []types.ToolDef,
	results []types.ToolCheckResult,
	force bool,
) []int {
	var idxs []int
	for j := range results {
		needs := force ||
			results[j].Status == types.StatusToolMissing ||
			results[j].Status == types.StatusToolOutdated
		if !needs {
			continue
		}
		if _, ok := i.resolveMethod(&required[j]); ok {
			idxs = append(idxs, j)
		}
	}
	return idxs
}

// confirmRequired asks the Prompter to install the selected required tools
// (autoYes skips the question) and returns its error, e.g. the --yes guidance when
// there is no operator to answer.
func (i *Installer) confirmRequired(
	ctx context.Context,
	required []types.ToolDef,
	idxs []int,
	autoYes bool,
) (bool, error) {
	names := make([]string, len(idxs))
	for k, idx := range idxs {
		names[k] = required[idx].Name
	}
	return i.confirm(
		ctx,
		fmt.Sprintf("Install missing tools (%s)?", strings.Join(names, ", ")),
		autoYes,
	)
}

// confirm reports yes without asking when autoYes is set; otherwise it asks the
// injected Prompter, failing with a coded error when none was injected.
func (i *Installer) confirm(
	ctx context.Context,
	question string,
	autoYes bool,
) (bool, error) {
	if autoYes {
		return true, nil
	}
	if i.prompter == nil {
		return false, apperr.New(
			apperr.CodeInvalidInput,
			"no Prompter was injected to confirm the install; pass --yes to install without "+
				"asking",
		)
	}
	return i.prompter.Confirm(ctx, question)
}

// reportSkipped logs and returns a CodeUnavailable error listing the required
// tools the user declined to install.
func reportSkipped(required []types.ToolDef, idxs []int, log interfaces.Logger) error {
	log.Info("Skipping tool installation")
	log.Error(fmt.Sprintf("Missing tools (%d):", len(idxs)))
	for _, idx := range idxs {
		msg := required[idx].Name
		if required[idx].FallbackURL != "" {
			msg += "\n    Install: " + required[idx].FallbackURL
		}
		log.Error("  * " + msg)
	}
	return apperr.New(apperr.CodeUnavailable, fmt.Sprintf(
		"%d required tool(s) missing from PATH -- install them and retry", len(idxs),
	))
}

// installAndVerify installs each selected tool, adds its bin dir to PATH, and
// re-checks it, returning an error if any tool is still unavailable afterwards.
func (i *Installer) installAndVerify(
	ctx context.Context,
	root string,
	det *Detector,
	required []types.ToolDef,
	idxs []int,
) error {
	var failures []string
	for _, idx := range idxs {
		tool := &required[idx]
		if err := i.Install(ctx, root, tool); err != nil {
			return toolError(
				tool,
				apperr.Wrap(err, apperr.CodeInternal, "install failed"),
			)
		}
		i.addInstallDirToPath(tool)
		if verify := det.Check(ctx, root, tool); verify.Status == types.StatusToolOK {
			i.log.Info(verify.Message + " (installed)")
		} else {
			failures = append(failures, tool.Name)
			i.log.Error(tool.Name + ": still not available after install")
		}
	}
	if len(failures) > 0 {
		return apperr.Internal(fmt.Sprintf(
			"%d tool(s) failed to install: %s",
			len(failures),
			strings.Join(failures, ", "),
		))
	}
	return nil
}

// toolError wraps a tool failure with its manual-install fallback URL.
func toolError(tool *types.ToolDef, cause error) error {
	msg := fmt.Sprintf("required tool %s: %s", tool.Name, cause)
	if tool.FallbackURL != "" {
		msg += "\n  Install manually: " + tool.FallbackURL
	}
	return apperr.Internal(msg)
}

// InstallTools is the tool-install entry point. It picks the pool the
// request targets — exactly the ci-marked tools (opts.CI), the named tools from
// every installable category, or, with neither, the optional tools — and runs
// the InstallSet flow over it. --ci with names is rejected: the CI set is
// all-or-nothing. --ci implies Force: CI must run the pinned binaries, never a
// runner's preinstalled copy of the same name at another version.
func (i *Installer) InstallTools(
	ctx context.Context,
	root string,
	cat *Catalog,
	opts types.InstallToolsOptions,
) error {
	switch {
	case opts.CI && len(opts.Names) > 0:
		return apperr.New(apperr.CodeInvalidInput,
			"--ci installs the whole CI set; drop the tool names or drop --ci")
	case opts.CI:
		opts.Force = true
		return i.InstallSet(ctx, root, cat.CI(), opts)
	case len(opts.Names) > 0:
		return i.InstallSet(ctx, root, cat.Installable(), opts)
	default:
		return i.InstallSet(ctx, root, cat.Optional(), opts)
	}
}

// InstallSet installs a pool of tools (the optional, installable, or CI set
// InstallTools picked): select targets (all, or the named subset), skip
// already-installed ones (unless Force), prompt (unless AutoYes), install each, and
// report failures.
func (i *Installer) InstallSet(
	ctx context.Context,
	root string,
	optional []types.ToolDef,
	opts types.InstallToolsOptions,
) error {
	targets, err := selectTargets(optional, opts.Names)
	if err != nil {
		return err
	}

	det := NewDetector(i.runner, i.log)
	var toInstall []types.ToolDef
	for j := range targets {
		// --force rebuilds even an OK tool (the post-Go-bump case: right version,
		// stale build-Go); otherwise skip anything already installed + current.
		if opts.Force ||
			det.Check(ctx, root, &targets[j]).Status != types.StatusToolOK {
			toInstall = append(toInstall, targets[j])
		}
	}
	if len(toInstall) == 0 {
		i.log.Info("All requested tools are already installed")
		return nil
	}

	names := toolNames(toInstall)
	yes, err := i.confirm(
		ctx,
		fmt.Sprintf(
			"Install %d tool(s) (%s)?",
			len(toInstall),
			strings.Join(names, ", "),
		),
		opts.AutoYes,
	)
	if err != nil {
		return err
	}
	if !yes {
		i.log.Info("Skipping installation")
		return nil
	}

	var failed []string
	for j := range toInstall {
		if err := i.Install(ctx, root, &toInstall[j]); err != nil {
			i.log.Warn(fmt.Sprintf("Failed to install %s: %v", toInstall[j].Name, err))
			failed = append(failed, toInstall[j].Name)
		}
	}
	if len(failed) > 0 {
		return apperr.New(apperr.CodeInternal, fmt.Sprintf(
			"failed to install %d tool(s): %s", len(failed), strings.Join(failed, ", "),
		))
	}
	i.log.Info("All tools installed successfully")
	return nil
}

// selectTargets returns all tools, or the named subset — a name matches a
// tool's display name or its binary — erroring on an unknown name.
func selectTargets(tools []types.ToolDef, names []string) ([]types.ToolDef, error) {
	if len(names) == 0 {
		return tools, nil
	}
	byName := make(map[string]types.ToolDef, 2*len(tools))
	for i := range tools {
		byName[tools[i].Binary] = tools[i]
		byName[tools[i].Name] = tools[i]
	}
	targets := make([]types.ToolDef, 0, len(names))
	for _, name := range names {
		t, ok := byName[name]
		if !ok {
			return nil, apperr.New(apperr.CodeInvalidInput, "unknown tool "+name+
				"; installable: "+strings.Join(toolNames(tools), ", "))
		}
		targets = append(targets, t)
	}
	return targets, nil
}

// toolNames extracts the display names of a tool slice.
func toolNames(tools []types.ToolDef) []string {
	names := make([]string, len(tools))
	for i := range tools {
		names[i] = tools[i].Name
	}
	return names
}

// invocation is the concrete command an install method resolves to.
type invocation struct {
	// command is the executable to run.
	command string

	// args are the arguments passed to command (after any rewrite).
	args []string

	// env holds extra environment variables (e.g. GOTOOLCHAIN=local).
	env []string
}

// resolveMethod returns the install method for the Installer's platform,
// preferring a cross-platform go-install over a native Windows installer (which
// is more fragile), and falling back to the "all" method.
func (i *Installer) resolveMethod(tool *types.ToolDef) (types.InstallMethod, bool) {
	if m, ok := tool.InstallMethods[i.platform]; ok {
		if i.platform == osWindows {
			if all, has := tool.InstallMethods["all"]; has && isGoInstall(all) {
				return all, true
			}
		}
		return m, true
	}
	if m, ok := tool.InstallMethods["all"]; ok {
		return m, true
	}
	return types.InstallMethod{}, false
}

// isGoInstall reports whether a method is a `go install` invocation.
func isGoInstall(m types.InstallMethod) bool {
	return m.Command == "go" && len(m.Args) > 0 && m.Args[0] == "install"
}

// buildInvocation turns an install method into the exact command to run: winget
// gains non-interactive flags, and go-install pins the local toolchain (with a
// Windows-ARM64 amd64 cross-compile rewrite). Pure — no execution or host I/O.
func (i *Installer) buildInvocation(method types.InstallMethod) invocation {
	inv := invocation{command: method.Command, args: append([]string{}, method.Args...)}
	switch {
	case method.Command == "winget":
		inv.args = append(
			inv.args,
			"--source", "winget",
			"--accept-source-agreements", "--accept-package-agreements",
		)
	case isGoInstall(method):
		inv.env = append(inv.env, "GOTOOLCHAIN=local")
		if i.platform == osWindows && i.arch == archARM64 {
			inv.env = append(
				inv.env,
				"GOARCH=amd64", "GOOS=windows", "GOBIN=", "GOMAXPROCS=1", "CGO_ENABLED=0",
			)
			inv.args = append([]string{inv.args[0], "-p", "1"}, inv.args[1:]...)
		}
	}
	return inv
}

// runWinget runs a winget install through the runner, treating the known
// "already installed" HRESULT exit codes as success.
func (i *Installer) runWinget(
	ctx context.Context,
	root, toolName string,
	args []string,
) error {
	err := i.runner.Run(ctx, root, "winget", args...)
	if err == nil {
		i.log.Info("installed via winget", "tool", toolName)
		return nil
	}

	var exitErr *exec.ExitError
	if apperr.As(err, &exitErr) {
		if desc, ok := ClassifyWingetExit(exitErr.ExitCode()); ok {
			i.log.Info(toolName + ": " + desc)
			return nil
		}
	}
	i.log.Error(fmt.Sprintf("winget install %s failed: %v", toolName, err))
	return apperr.Wrap(err, apperr.CodeInternal, "winget install "+toolName)
}

// wingetAlreadyInstalledCodes maps a winget HRESULT exit code that means the package is
// already present to its human-readable description; ok is false for any other code.
func wingetAlreadyInstalledCodes(code uint32) (string, bool) {
	switch code {
	case 0x8A15002B:
		return "already installed, no update available", true
	case 0x8A150061:
		return "package already installed", true
	case 0x8A15010D:
		return "another version already installed", true
	case 0x8A15004F:
		return "upgrade version not newer than installed", true
	default:
		return "", false
	}
}

// ClassifyWingetExit reports whether an exit code is a success-like "already
// installed" condition, with its description.
func ClassifyWingetExit(exitCode int) (string, bool) {
	//nolint:gosec // intentional uint32 cast; HRESULT values are unsigned 32-bit
	return wingetAlreadyInstalledCodes(uint32(exitCode))
}

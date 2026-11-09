package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// catalogFromEntries builds a Catalog from raw manifest tool entries.
func catalogFromEntries(t *testing.T, entries ...types.ToolEntry) *toolchain.Catalog {
	t.Helper()

	cat, err := toolchain.NewCatalog(entries)
	require.NoError(t, err)

	return cat
}

// goPrereqEntry is a versionless "go" prerequisite reused by the Provision flows.
func goPrereqEntry() types.ToolEntry {
	return types.ToolEntry{
		Name:     "Go",
		Binary:   textGoLower,
		Category: types.CategoryPrerequisite,
		Required: true,
	}
}

// wireRequiredEntry is a go-install required tool carrying a manual-install
// fallback URL, so the fallback-formatting branches of reportSkipped and toolError
// are exercised.
func wireRequiredEntry() types.ToolEntry {
	return types.ToolEntry{
		Name:        textWire,
		Binary:      textWire,
		Category:    types.CategoryRequired,
		Required:    true,
		FallbackURL: "https://example.test/wire",
		Install: map[string]types.InstallMethodEntry{
			textAll: {Kind: types.InstallKindGoInstall, Package: "x/wire@v1"},
		},
	}
}

// --- NewCatalog / projection error paths ---

// TestClientsToolchainNewCatalog_ProjectionErrorsPerCategory tests that NewCatalog fails
// when any category holds a tool with an unparseable min_version.
//
// Why this test is important:
//   - NewCatalog is the load-time gate that turns manifest DATA into tool specs; a
//     malformed min_version in any category must fail loudly at composition time,
//     not surface later as a bad version comparison during setup.
//
// What it tests:
//   - a bad min_version in the prerequisite, required, or optional category each
//     makes NewCatalog return an error
func TestClientsToolchainNewCatalog_ProjectionErrorsPerCategory(t *testing.T) {
	t.Parallel()

	for _, cat := range []types.ToolCategory{
		types.CategoryPrerequisite,
		types.CategoryRequired,
		types.CategoryOptional,
	} {
		_, err := toolchain.NewCatalog([]types.ToolEntry{
			{Name: "Bad", Binary: textBad, Category: cat, MinVersion: "not-a-version"},
		})
		require.Errorf(t, err, "category %q", cat)
	}
}

// TestClientsToolchainProjectLifecycleTools_HappyAndError tests ProjectLifecycleTools
// over the real manifest and over a manifest with an unparseable min_version.
//
// Why this test is important:
//   - ProjectLifecycleTools is the flat projection used to enumerate every managed
//     tool; it must project the real manifest and reject a malformed one rather
//     than emit a half-built spec list.
//
// What it tests:
//   - the embedded manifest projects to a non-empty list with no error; a bad
//     min_version yields an error
func TestClientsToolchainProjectLifecycleTools_HappyAndError(t *testing.T) {
	t.Parallel()

	defs, err := toolchain.ProjectLifecycleTools(toolEntries(t))
	require.NoError(t, err)
	assert.NotEmpty(t, defs)

	_, err = toolchain.ProjectLifecycleTools([]types.ToolEntry{
		{
			Name:       "Bad",
			Binary:     textBad,
			Category:   types.CategoryRequired,
			MinVersion: textNope,
		},
	})
	require.Error(t, err)
}

// --- Detector: ListOptional + CheckPrerequisites Outdated ---

// TestClientsToolchainDetector_ListOptional tests that ListOptional probes every
// optional tool's presence.
//
// Why this test is important:
//   - A tool listing reports install status per optional tool; ListOptional
//     must query each one (installed vs not) so the list is accurate.
//
// What it tests:
//   - ListOptional checks the presence of every tool in the list (one present, one
//     absent) without error
func TestClientsToolchainDetector_ListOptional(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Exists(textPgcli).Return(true)
	r.EXPECT().Exists(textGrpcurl).Return(false)

	det := toolchain.NewDetector(r, fixtures.NopLogger())
	det.ListOptional(context.Background(), textRoot, []types.ToolDef{
		{Name: textPgcli, Binary: textPgcli, Description: "Postgres CLI"},
		{Name: textGrpcurl, Binary: textGrpcurl, Description: "gRPC CLI"},
	})
}

// TestClientsToolchainDetector_CheckPrerequisites_Outdated tests that a
// present-but-too-old prerequisite is reported as a missing dependency.
//
// Why this test is important:
//   - A prerequisite below its minimum version is as blocking as an absent one;
//     the check must classify it as outdated and surface it, not pass it as OK.
//
// What it tests:
//   - a "go" prerequisite requiring 1.30 but reporting 1.26 makes CheckPrerequisites
//     return an error naming it
func TestClientsToolchainDetector_CheckPrerequisites_Outdated(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Exists(textGoLower).Return(true)
	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), textGoLower, textVersion).
		Return(interfaces.CmdResult{Stdout: []byte("1.26.0")})

	det := toolchain.NewDetector(commandRunner, fixtures.NopLogger())
	err := det.CheckPrerequisites(context.Background(), textRoot, []types.ToolDef{{
		Name: "Go", Binary: textGoLower,
		MinVersion:   &types.Version{Major: 1, Minor: 30},
		VersionCmd:   []string{textGoLower, textVersion},
		VersionParse: types.ParseFirstVersion,
		FallbackURL:  "https://go.dev/dl/",
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Go")
}

// --- parseGoVersion fallback (via the resolved parser) ---

// TestClientsToolchainGoVersionParse_Fallback tests that the go-version parser falls
// back to the first-version extractor when the input lacks a "goN" token.
//
// Why this test is important:
//   - The go-version strategy must still extract a version from atypical output
//     (no "go" prefix) rather than returning empty and forcing a false "outdated".
//
// What it tests:
//   - the resolved go parser returns "1.26.0" for bare "1.26.0" input
func TestClientsToolchainGoVersionParse_Fallback(t *testing.T) {
	t.Parallel()

	cat, err := toolchain.NewCatalog(toolEntries(t))
	require.NoError(t, err)

	goDef, ok := cat.Find(textGoLower)
	require.True(t, ok)
	assert.Equal(t, "1.26.0", goDef.VersionParse("1.26.0"))
}

// --- winget generic error ---

// TestClientsToolchainInstaller_Install_WingetGenericError tests that a non-exit-code
// winget failure is surfaced as an error.
//
// Why this test is important:
//   - Only the known "already installed" HRESULT exit codes are success-like; any
//     other winget failure must propagate so setup does not report a failed install
//     as success.
//
// What it tests:
//   - a winget install whose runner returns a plain (non-ExitError) error makes
//     Install return an error
func TestClientsToolchainInstaller_Install_WingetGenericError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Run(
		gomock.Any(), textRoot, "winget",
		textInstall, "Foo.Bar",
		"--source", "winget",
		"--accept-source-agreements", "--accept-package-agreements",
	).Return(assert.AnError)

	tool := &types.ToolDef{
		Name: "foo",
		InstallMethods: map[string]types.InstallMethod{
			textWindows: {Command: "winget", Args: []string{textInstall, "Foo.Bar"}},
		},
	}
	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textWindows,
		denyPrompter(t),
		toolchain.WithArch("amd64"),
	)
	require.Error(t, inst.Install(context.Background(), textRoot, tool))
}

// --- Provision flows via injected platform ---

// TestClientsToolchainProvision_WindowsCheckOnly tests the Windows check-only setup
// path: the go-install bin dir and its windows_amd64 cross-compile subdir are added to
// PATH, the MSYS2 probe runs, and nothing is installed.
//
// Why this test is important:
//   - Windows setup must prepare PATH for both native and cross-compiled Go tools
//     and probe MSYS2, all without installing in check-only mode; the platform is
//     injected so this runs deterministically off a Windows host.
//
// What it tests:
//   - after a windows/arm64 check-only Provision, GOBIN and GOBIN/windows_amd64 are
//     on PATH and Provision returns nil
func TestClientsToolchainProvision_WindowsCheckOnly(t *testing.T) {
	gobin := t.TempDir()
	t.Setenv(textPath, textUsrBin)
	t.Setenv(textGobin, gobin)

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	registerToolScanMock(r)

	inst := toolchain.NewInstaller(
		r,
		fixtures.NopLogger(),
		textWindows,
		denyPrompter(t),
		toolchain.WithArch("arm64"),
	)
	require.NoError(
		t,
		inst.Provision(
			context.Background(),
			textRoot,
			provisionCatalog(t),
			&types.InstallToolsOptions{AutoYes: false, CheckOnly: true},
		),
	)

	assert.True(t, onPathBoost(filepath.Join(gobin, "windows_amd64")),
		"windows_amd64 subdir must be on PATH, got %q", os.Getenv(textPath))
}

// TestClientsToolchainProvision_GoBinUnresolvable tests that Provision tolerates an
// unresolvable go-install bin dir (GOBIN/GOPATH unset and HOME empty).
//
// Why this test is important:
//   - When the go bin dir cannot be resolved, ensureGoBinInPath must return quietly
//     rather than panic or add an empty PATH entry; setup should still complete.
//
// What it tests:
//   - with GOBIN, GOPATH, and HOME all empty, a check-only Provision returns nil
func TestClientsToolchainProvision_GoBinUnresolvable(t *testing.T) {
	t.Setenv(textGobin, "")
	t.Setenv("GOPATH", "")
	t.Setenv("HOME", "")

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	registerToolScanMock(r)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), textLinuxApt, denyPrompter(t))
	require.NoError(
		t,
		inst.Provision(
			context.Background(),
			textRoot,
			provisionCatalog(t),
			&types.InstallToolsOptions{AutoYes: false, CheckOnly: true},
		),
	)
}

// TestClientsToolchainProvision_UserDeclines tests that declining the install prompt
// reports the still-missing required tools as an error.
//
// Why this test is important:
//   - When the developer answers "no", setup must not proceed but must still fail
//     with the list of missing tools so the environment gap is visible, not
//     silently swallowed.
//
// What it tests:
//   - with a missing installable required tool and a "n" answer, Provision returns
//     an error naming the missing tool
func TestClientsToolchainProvision_UserDeclines(t *testing.T) {
	t.Setenv(textGobin, t.TempDir())

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Exists(textGoLower).Return(true)
	commandRunner.EXPECT().Exists(textWire).Return(false)

	prompter := mocks.NewMockPrompter(ctrl)
	prompter.EXPECT().Confirm(gomock.Any(), gomock.Any()).Return(false, nil)

	cat := catalogFromEntries(t, goPrereqEntry(), wireRequiredEntry())
	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textLinuxApt,
		prompter,
	)
	err := inst.Provision(
		context.Background(),
		textRoot,
		cat,
		&types.InstallToolsOptions{AutoYes: false, CheckOnly: false},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), textMissing)
}

// TestClientsToolchainProvision_InstallFailure tests that a failed install command is
// surfaced with the tool's manual-install fallback.
//
// Why this test is important:
//   - If the install command fails, setup must abort with a clear, tool-named error
//     (including the fallback URL), not continue as if the tool were present.
//
// What it tests:
//   - a go-install that returns an error makes Provision return an error naming the tool
func TestClientsToolchainProvision_InstallFailure(t *testing.T) {
	t.Setenv(textGobin, t.TempDir())

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Exists(textGoLower).Return(true)
	gomock.InOrder(
		commandRunner.EXPECT().Exists(textWire).Return(false),
		commandRunner.EXPECT().RunWithEnv(
			gomock.Any(), textRoot, []string{textGotoolchainLocal},
			textGoLower, textInstall, "x/wire@v1",
		).Return(assert.AnError),
	)

	cat := catalogFromEntries(t, goPrereqEntry(), wireRequiredEntry())
	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textLinuxApt,
		denyPrompter(t),
	)
	err := inst.Provision(
		context.Background(),
		textRoot,
		cat,
		&types.InstallToolsOptions{AutoYes: true, CheckOnly: false},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), textWire)
}

// TestClientsToolchainProvision_VerifyFailure tests that a tool still missing after a
// "successful" install is reported as a failure.
//
// Why this test is important:
//   - An install command can exit 0 yet leave the binary off PATH; the post-install
//     re-check is what catches that, and setup must fail rather than claim success.
//
// What it tests:
//   - install returns nil but the verify re-check reports the tool absent, so
//     Provision returns an error naming the tool
func TestClientsToolchainProvision_VerifyFailure(t *testing.T) {
	t.Setenv(textGobin, t.TempDir())

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Exists(textGoLower).Return(true)
	gomock.InOrder(
		commandRunner.EXPECT().Exists(textWire).Return(false),
		commandRunner.EXPECT().RunWithEnv(
			gomock.Any(), textRoot, []string{textGotoolchainLocal},
			textGoLower, textInstall, "x/wire@v1",
		).Return(nil),
		commandRunner.EXPECT().
			Exists(textWire).
			Return(false), // still missing after install
	)

	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textLinuxApt,
		denyPrompter(t),
	)
	err := inst.Provision(
		context.Background(),
		textRoot,
		provisionCatalog(t),
		&types.InstallToolsOptions{AutoYes: true, CheckOnly: false},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), textWire)
}

// TestClientsToolchainProvision_MissingToolNoMethodSkipped tests that a missing required
// tool with no install method for this platform is not force-installed.
//
// Why this test is important:
//   - Some required tools (e.g. a compiler) have no automated install on a given
//     platform; setup must report them rather than run a half-built command, and
//     with no installable target Provision completes without erroring on the prereqs.
//
// What it tests:
//   - a missing required tool lacking any install method leaves nothing to install,
//     so Provision returns nil (prereqs satisfied)
func TestClientsToolchainProvision_MissingToolNoMethodSkipped(t *testing.T) {
	t.Setenv(textGobin, t.TempDir())

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Exists(textGoLower).Return(true)
	commandRunner.EXPECT().
		Exists(textGcc).
		Return(false)
		// missing, no install method → skipped
	commandRunner.EXPECT().Exists("present").Return(true) // already OK → not selected

	cat := catalogFromEntries(
		t,
		goPrereqEntry(), types.ToolEntry{
			Name:     "GCC",
			Binary:   textGcc,
			Category: types.CategoryRequired,
			Required: true,
		}, types.ToolEntry{
			Name:     "Present",
			Binary:   "present",
			Category: types.CategoryRequired,
			Required: true,
		},
	)
	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textLinuxApt,
		denyPrompter(t),
	)
	require.NoError(
		t,
		inst.Provision(
			context.Background(),
			textRoot,
			cat,
			&types.InstallToolsOptions{AutoYes: true, CheckOnly: false},
		),
	)
}

// TestClientsToolchainProvision_DarwinNoBrew tests that a macOS setup without Homebrew
// warns and continues rather than aborting.
//
// Why this test is important:
//   - Homebrew is the primary macOS install channel; its absence should degrade to
//     a warning (some tools need manual install) rather than failing the whole
//     setup before prerequisites are even checked.
//
// What it tests:
//   - with platform darwin and brew absent, a check-only Provision still completes
//     (returns nil) after warning
func TestClientsToolchainProvision_DarwinNoBrew(t *testing.T) {
	t.Setenv(textGobin, t.TempDir())

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Exists("brew").Return(false)
	r.EXPECT().Exists(textGoLower).Return(true)
	r.EXPECT().Exists(textWire).Return(true)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "darwin", denyPrompter(t))
	require.NoError(
		t,
		inst.Provision(
			context.Background(),
			textRoot,
			provisionCatalog(t),
			&types.InstallToolsOptions{AutoYes: false, CheckOnly: true},
		),
	)
}

// TestClientsToolchainInstallSet_AllAlreadyInstalled tests the no-op path when every
// requested optional tool is already present.
//
// Why this test is important:
//   - A tool install must not prompt or run any install command when the
//     requested tools are already on PATH; it should report that and return cleanly.
//
// What it tests:
//   - with the only requested tool already present, InstallSet runs no install
//     command and returns nil
func TestClientsToolchainInstallSet_AllAlreadyInstalled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Exists(textPgcli).Return(true)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), textLinuxApt, denyPrompter(t))
	require.NoError(t, inst.InstallSet(
		context.Background(),
		textRoot,
		[]types.ToolDef{
			{Name: textPgcli, Binary: textPgcli},
		},
		types.InstallToolsOptions{AutoYes: true},
	))
}

// TestClientsToolchainInstallSet_DryRun tests the dry-run path of a tool install.
//
// Why this test is important:
//   - `search tools install --for … --dry-run` is how CI's tool list is read off the
//     CLI; a dry run that installed, or one that reported nothing for a missing tool,
//     would each mislead the reader.
//
// What it tests:
//   - with the requested tool absent and DryRun set, InstallSet runs no install command,
//     prompts nothing (the prompter denies) and returns nil.
func TestClientsToolchainInstallSet_DryRun(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Exists(textPgcli).Return(false)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), textLinuxApt, denyPrompter(t))
	require.NoError(t, inst.InstallSet(
		context.Background(),
		textRoot,
		[]types.ToolDef{{
			Name: textPgcli, Binary: textPgcli,
			InstallMethods: map[string]types.InstallMethod{
				textLinuxApt: {
					Command: "apt-get",
					Args:    []string{textInstall, textPgcli},
				},
			},
		}},
		types.InstallToolsOptions{DryRun: true},
	))
}

// TestClientsToolchainProvision_WindowsArm64InstallAddsAmd64Path tests that installing a
// go tool on windows/arm64 adds the cross-compiled windows_amd64 dir to PATH for
// verification.
//
// Why this test is important:
//   - The Windows-ARM64 workaround cross-compiles Go tools into a windows_amd64
//     subdir; the post-install verify can only find the binary if that dir is added
//     to PATH.
//
// What it tests:
//   - after a windows/arm64 install, GOBIN/windows_amd64 is on PATH and Provision
//     succeeds when the verify re-check finds the tool
func TestClientsToolchainProvision_WindowsArm64InstallAddsAmd64Path(t *testing.T) {
	gobin := t.TempDir()
	t.Setenv(textPath, textUsrBin)
	t.Setenv(textGobin, gobin)

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Exists(textGoLower).Return(true)
	gomock.InOrder(
		commandRunner.EXPECT().Exists(textWire).Return(false),
		commandRunner.EXPECT().RunWithEnv(
			gomock.Any(), textRoot, gomock.Any(),
			textGoLower, textInstall, "-p", "1", "x/wire@v1",
		).Return(nil),
		commandRunner.EXPECT().Exists(textWire).Return(true),
	)

	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textWindows,
		denyPrompter(t),
		toolchain.WithArch("arm64"),
	)
	require.NoError(
		t,
		inst.Provision(
			context.Background(),
			textRoot,
			provisionCatalog(t),
			&types.InstallToolsOptions{AutoYes: true, CheckOnly: false},
		),
	)
	assert.True(t, onPathBoost(filepath.Join(gobin, "windows_amd64")))
}

// TestClientsToolchainProvision_WindowsPowershellTools tests that installing
// PowerShell-based Windows tools adds their install dirs to PATH (MSYS2 for gcc,
// LOCALAPPDATA for others).
//
// Why this test is important:
//   - Windows tools installed via PowerShell land in tool-specific directories that
//     the verify step must be able to find; gcc goes to MSYS2 and other tools to
//     LOCALAPPDATA/Programs, and the wrong PATH addition breaks verification.
//
// What it tests:
//   - after installing a non-gcc PowerShell tool, LOCALAPPDATA/Programs/<tool> is on
//     PATH; Provision succeeds when both tools verify present
func TestClientsToolchainProvision_WindowsPowershellTools(t *testing.T) {
	appData := t.TempDir()
	t.Setenv(textPath, textUsrBin)
	t.Setenv("LOCALAPPDATA", appData)

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Exists(textGoLower).Return(true)
	gomock.InOrder(
		commandRunner.EXPECT().Exists(textGcc).Return(false),
		commandRunner.EXPECT().Exists("mytool").Return(false),
		commandRunner.EXPECT().RunWithEnv(
			gomock.Any(), textRoot, gomock.Any(), textPowershell, "-c", "install-gcc",
		).Return(nil),
		commandRunner.EXPECT().Exists(textGcc).Return(true),
		commandRunner.EXPECT().RunWithEnv(
			gomock.Any(), textRoot, gomock.Any(), textPowershell, "-c", "install-mytool",
		).Return(nil),
		commandRunner.EXPECT().Exists("mytool").Return(true),
	)

	testClientsToolchainProvisionWindowsChecksTools(t, appData, commandRunner)
}

// testClientsToolchainProvisionWindowsChecksTools continues
// TestClientsToolchainProvision_WindowsPowershellTools.
func testClientsToolchainProvisionWindowsChecksTools(
	t *testing.T,
	appData string,
	commandRunner *mocks.MockCommandRunner,
) {
	t.Helper()

	cat := catalogFromEntries(
		t, goPrereqEntry(), types.ToolEntry{
			Name:     "GCC",
			Binary:   textGcc,
			Category: types.CategoryRequired,
			Required: true,
			Install: map[string]types.InstallMethodEntry{
				textWindows: {
					Kind:    types.InstallKindCommand,
					Command: textPowershell,
					Args:    []string{"-c", "install-gcc"},
				},
			},
		}, types.ToolEntry{
			Name:     "MyTool",
			Binary:   "mytool",
			Category: types.CategoryRequired,
			Required: true,
			Install: map[string]types.InstallMethodEntry{
				textWindows: {
					Kind:    types.InstallKindCommand,
					Command: textPowershell,
					Args:    []string{"-c", "install-mytool"},
				},
			},
		},
	)

	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textWindows,
		denyPrompter(t),
		toolchain.WithArch("amd64"),
	)
	require.NoError(
		t,
		inst.Provision(
			context.Background(),
			textRoot,
			cat,
			&types.InstallToolsOptions{AutoYes: true, CheckOnly: false},
		),
	)
	assert.True(t, onPathBoost(filepath.Join(appData, "Programs", "mytool")),
		"LOCALAPPDATA/Programs/mytool must be on PATH, got %q", os.Getenv(textPath))
}

// --- InstallSet flows ---

// TestClientsToolchainInstallSet_NamedSubsetFailure tests that a named optional tool
// whose install fails is reported.
//
// Why this test is important:
//   - A named tool install targets a named subset; a failed install must
//     be collected and returned as an error rather than reported as success.
//
// What it tests:
//   - installing the named "grpcurl" whose command fails makes InstallSet
//     return an error naming it
func TestClientsToolchainInstallSet_NamedSubsetFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Exists(textGrpcurl).Return(false)
	commandRunner.EXPECT().RunWithEnv(
		gomock.Any(), textRoot, []string{textGotoolchainLocal},
		textGoLower, textInstall, textGrpcurlV1,
	).Return(assert.AnError)

	optional := []types.ToolDef{
		{Name: textPgcli, Binary: textPgcli},
		{
			Name:   textGrpcurl,
			Binary: textGrpcurl,
			InstallMethods: map[string]types.InstallMethod{
				textAll: {
					Command: textGoLower,
					Args:    []string{textInstall, textGrpcurlV1},
				},
			},
		},
	}
	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textLinuxApt,
		denyPrompter(t),
	)
	err := inst.InstallSet(
		context.Background(),
		textRoot,
		optional,
		types.InstallToolsOptions{Names: []string{textGrpcurl}, AutoYes: true},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), textGrpcurl)
}

// TestClientsToolchainInstallSet_UserDeclines tests that declining the optional-install
// prompt installs nothing and returns nil.
//
// Why this test is important:
//   - A tool install must respect a "no" answer: nothing is installed and
//     the command succeeds quietly rather than erroring or proceeding.
//
// What it tests:
//   - a missing optional tool with a "n" answer makes InstallSet a no-op
//     (returns nil, no install command run)
func TestClientsToolchainInstallSet_UserDeclines(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Exists(textGrpcurl).Return(false)

	prompter := mocks.NewMockPrompter(ctrl)
	prompter.EXPECT().Confirm(gomock.Any(), gomock.Any()).Return(false, nil)

	optional := []types.ToolDef{
		{
			Name:   textGrpcurl,
			Binary: textGrpcurl,
			InstallMethods: map[string]types.InstallMethod{
				textAll: {
					Command: textGoLower,
					Args:    []string{textInstall, textGrpcurlV1},
				},
			},
		},
	}
	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textLinuxApt,
		prompter,
	)
	require.NoError(t, inst.InstallSet(
		context.Background(),
		textRoot,
		optional,
		types.InstallToolsOptions{AutoYes: false},
	))
}

// onPathBoost reports whether want is a PATH entry.
func onPathBoost(want string) bool {
	for _, e := range filepath.SplitList(os.Getenv(textPath)) {
		if strings.EqualFold(e, want) {
			return true
		}
	}

	return false
}

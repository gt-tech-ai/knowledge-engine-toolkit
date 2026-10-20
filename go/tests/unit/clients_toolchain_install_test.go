package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// provisionCatalog builds a catalog with one (versionless) prerequisite and one
// go-install required tool, for the Provision orchestrator tests.
func provisionCatalog(t *testing.T) *toolchain.Catalog {
	t.Helper()
	cat, err := toolchain.NewCatalog([]toolchain.ToolEntry{
		{
			Name:     "Go",
			Binary:   "go",
			Category: toolchain.CategoryPrerequisite,
			Required: true,
		},
		{
			Name:     "wire",
			Binary:   "wire",
			Category: toolchain.CategoryRequired,
			Required: true,
			Install: map[string]toolchain.InstallMethodEntry{
				"all": {Kind: toolchain.InstallKindGoInstall, Package: "x/wire@v1"},
			},
		},
	})
	require.NoError(t, err)
	return cat
}

// TestClientsToolchainInstaller_Install_RoutesExactCommand verifies a single install
// command.
//
// Why this test is important:
//   - The Installer is the only thing that runs install commands; an incorrect
//     command or missing GOTOOLCHAIN env silently installs the wrong thing or
//     bypasses the pinned toolchain.
//
// What it tests:
//   - A go-install method routes `go install <pkg>` through the runner with
//     GOTOOLCHAIN=local in the environment.
func TestClientsToolchainInstaller_Install_RoutesExactCommand(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().
		RunWithEnv(
			gomock.Any(),
			"/root",
			[]string{"GOTOOLCHAIN=local"},
			"go",
			"install",
			"pkg@v1",
		).
		Return(nil)

	tool := &types.ToolDef{
		Name: "grpcurl", Binary: "grpcurl",
		InstallMethods: map[string]types.InstallMethod{
			"all": {Command: "go", Args: []string{"install", "pkg@v1"}},
		},
	}

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "linux-apt", denyPrompter(t))
	require.NoError(t, inst.Install(context.Background(), "/root", tool))
}

// TestClientsToolchainInstaller_InstallSet_SkipsInstalled verifies the dev-tool
// orchestrator.
//
// Why this test is important:
//   - A tool install must not reinstall tools the developer already has,
//     and must only install after confirmation; reinstalling wastes time and can
//     clobber a working install.
//
// What it tests:
//   - An already-present tool is skipped; a missing one is installed (auto-yes).
func TestClientsToolchainInstaller_InstallSet_SkipsInstalled(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)

	optional := []types.ToolDef{
		{
			Name:   "pgcli",
			Binary: "pgcli",
			InstallMethods: map[string]types.InstallMethod{
				"all": {Command: "pip", Args: []string{"install", "pgcli"}},
			},
		},
		{
			Name:   "grpcurl",
			Binary: "grpcurl",
			InstallMethods: map[string]types.InstallMethod{
				"all": {Command: "go", Args: []string{"install", "grpcurl@v1"}},
			},
		},
	}

	// pgcli already present (skip); grpcurl missing -> installed.
	r.EXPECT().Exists("pgcli").Return(true)
	r.EXPECT().Exists("grpcurl").Return(false)
	r.EXPECT().
		RunWithEnv(
			gomock.Any(),
			"/root",
			[]string{"GOTOOLCHAIN=local"},
			"go",
			"install",
			"grpcurl@v1",
		).
		Return(nil)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "linux-apt", denyPrompter(t))
	err := inst.InstallSet(
		context.Background(),
		"/root",
		optional,
		types.InstallToolsOptions{AutoYes: true},
	)
	require.NoError(t, err)
}

// TestClientsToolchainInstaller_Provision_CheckOnly covers a check-only setup.
//
// Why this test is important:
//   - Check-only mode must report what's missing without mutating the
//     environment; installing in --check mode would surprise the developer.
//
// What it tests:
//   - With a present prerequisite and a missing required tool, Provision
//     (checkOnly=true) returns an error naming the missing tool and installs nothing.
func TestClientsToolchainInstaller_Provision_CheckOnly(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Exists("go").Return(true)    // prerequisite present
	r.EXPECT().Exists("wire").Return(false) // required tool missing

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "linux-apt", denyPrompter(t))
	err := inst.Provision(context.Background(), "/root", provisionCatalog(t), false, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wire")
}

// TestClientsToolchainInstaller_Provision_InstallsMissing covers the full setup install
// path.
//
// Why this test is important:
//   - Provision must install a missing required tool AND re-verify it; skipping
//     the post-install check would let setup report success for a tool that
//     failed to land on PATH.
//
// What it tests:
//   - A missing required tool is scanned, installed via go-install, and
//     re-checked as present (auto-yes), yielding no error.
func TestClientsToolchainInstaller_Provision_InstallsMissing(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Exists("go").Return(true)
	gomock.InOrder(
		r.EXPECT().Exists("wire").Return(false), // scan: missing
		r.EXPECT().
			RunWithEnv(
				gomock.Any(),
				"/root",
				[]string{"GOTOOLCHAIN=local"},
				"go",
				"install",
				"x/wire@v1",
			).
			Return(nil),
		r.EXPECT().Exists("wire").Return(true), // verify: now present
	)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "linux-apt", denyPrompter(t))
	err := inst.Provision(context.Background(), "/root", provisionCatalog(t), true, false)
	require.NoError(t, err)
}

// TestClientsToolchainInstaller_InstallSet_ForceReinstalls covers a forced tool install.
//
// Why this test is important:
//   - After a Go-toolchain bump the go-install binaries are the right version but
//     built with the old Go; --force must rebuild them, bypassing the
//     already-present skip that InstallSet normally applies.
//
// What it tests:
//   - With Force set, a present tool's presence check is short-circuited and the
//     install command runs anyway.
func TestClientsToolchainInstaller_InstallSet_ForceReinstalls(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	optional := []types.ToolDef{
		{
			Name:   "pgcli",
			Binary: "pgcli",
			InstallMethods: map[string]types.InstallMethod{
				"all": {Command: "pip", Args: []string{"install", "pgcli"}},
			},
		},
	}
	// No Exists("pgcli"): Force short-circuits the presence check; install runs.
	r.EXPECT().
		RunWithEnv(gomock.Any(), "/root", gomock.Any(), "pip", "install", "pgcli").
		Return(nil)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "linux-apt", denyPrompter(t))
	err := inst.InstallSet(
		context.Background(),
		"/root",
		optional,
		types.InstallToolsOptions{AutoYes: true, Force: true},
	)
	require.NoError(t, err)
}

// TestClientsToolchainInstaller_Provision_ForceReinstallsPresent covers a forced setup.
//
// Why this test is important:
//   - The required dev tools (golangci-lint, gofumpt, …) are go-install binaries; after a
//     Go bump they are the right version but built with the old Go and must be rebuilt.
//     Provision's default skips present+OK tools, so WithForce must override that —
//     otherwise setup silently leaves stale-build tools in place.
//
// What it tests:
//   - With WithForce, a present + version-OK required tool is reinstalled and
//     re-verified.
func TestClientsToolchainInstaller_Provision_ForceReinstallsPresent(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Exists("go").Return(true) // prerequisite present
	gomock.InOrder(
		r.EXPECT().Exists("wire").Return(true), // scan: present + OK
		r.EXPECT().
			RunWithEnv(
				gomock.Any(),
				"/root",
				[]string{"GOTOOLCHAIN=local"},
				"go",
				"install",
				"x/wire@v1",
			).
			Return(nil), // force → rebuild anyway
		r.EXPECT().Exists("wire").Return(true), // verify: present
	)

	inst := toolchain.NewInstaller(
		r, fixtures.NopLogger(), "linux-apt", denyPrompter(t), toolchain.WithForce(true),
	)
	err := inst.Provision(context.Background(), "/root", provisionCatalog(t), true, false)
	require.NoError(t, err)
}

// TestClientsToolchainInstaller_Install_NoMethod covers the no-applicable-method path.
//
// Why this test is important:
//   - A tool with no install method for the host (e.g. gcc off Windows) must
//     fail fast with a clear error, not run a half-built command.
//
// What it tests:
//   - Install errors with a "no install method" message and makes no runner call.
func TestClientsToolchainInstaller_Install_NoMethod(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl) // strict: no runner calls

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "darwin", denyPrompter(t))
	err := inst.Install(context.Background(), "/root",
		&types.ToolDef{Name: "gcc", Binary: "gcc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no install method")
}

// TestClientsToolchainPlatformKey_NonEmpty covers the platform key helper.
//
// Why this test is important:
//   - An empty platform key would resolve no install method for any tool.
//
// What it tests:
//   - PlatformKey() resolves to a non-empty key on the host.
func TestClientsToolchainPlatformKey_NonEmpty(t *testing.T) {
	t.Parallel()
	assert.NotEmpty(t, toolchain.PlatformKey())
}

// TestClientsToolchainInstaller_InstallSet_UnknownName covers the unknown-name guard.
//
// Why this test is important:
//   - A mistyped tool name must fail with a clear, named error rather than
//     silently installing nothing.
//
// What it tests:
//   - InstallSet with an unknown name returns an error naming that tool.
func TestClientsToolchainInstaller_InstallSet_UnknownName(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "darwin", denyPrompter(t))
	err := inst.InstallSet(
		context.Background(),
		"/root",
		[]types.ToolDef{
			{Name: "pgcli", Binary: "pgcli"},
		},
		types.InstallToolsOptions{Names: []string{"bogus"}, AutoYes: true},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

// goInstallEntry is a go-install lifecycle tool in the given category, marked
// ci when inCI is set, for the `--ci` selection tests.
func goInstallEntry(
	binary string,
	cat toolchain.ToolCategory,
	inCI bool,
) toolchain.ToolEntry {
	return toolchain.ToolEntry{
		Name: binary, Binary: binary, Category: cat, CI: inCI,
		Install: map[string]toolchain.InstallMethodEntry{
			"all": {
				Kind:    toolchain.InstallKindGoInstall,
				Package: "x/" + binary + "@v1.0.0",
			},
		},
	}
}

// TestClientsToolchainInstallTools_CIInstallsExactlyTheCISet tests a CI tool install.
//
// Why this test is important:
//   - CI provisions its whole toolchain with `--ci`; the flag must install every
//     tool the manifest marks `ci: true` — required and optional alike — and
//     nothing else, or CI drifts from the pinned set in either direction.
//
// What it tests:
//   - With a catalog mixing ci and non-ci tools across the required and optional
//     categories, InstallTools{CI} runs `go install` for exactly the two ci tools
//     (the strict runner mock fails on any other install), without first probing
//     whether they are present: --ci implies --force, so the pinned versions always
//     replace a runner's preinstalled copies.
//   - --ci combined with tool names is rejected before anything is installed.
//   - With every ci tool already present, --ci (with or without --force) still
//     reinstalls exactly the two ci tools.
func TestClientsToolchainInstallTools_CIInstallsExactlyTheCISet(t *testing.T) {
	t.Parallel()

	cat := catalogFromEntries(
		t,
		goPrereqEntry(),
		goInstallEntry("req-ci", toolchain.CategoryRequired, true),
		goInstallEntry("req-local", toolchain.CategoryRequired, false),
		goInstallEntry("opt-ci", toolchain.CategoryOptional, true),
		goInstallEntry("opt-local", toolchain.CategoryOptional, false),
	)

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	for _, bin := range []string{"req-ci", "opt-ci"} {
		r.EXPECT().
			RunWithEnv(gomock.Any(), "/root", []string{"GOTOOLCHAIN=local"},
				"go", "install", "x/"+bin+"@v1.0.0").
			Return(nil)
	}

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "linux-apt", denyPrompter(t))
	require.NoError(
		t,
		inst.InstallTools(
			context.Background(),
			"/root",
			cat,
			types.InstallToolsOptions{CI: true, AutoYes: true},
		),
	)

	err := inst.InstallTools(
		context.Background(),
		"/root",
		cat,
		types.InstallToolsOptions{
			CI:      true,
			Names:   []string{"req-local"},
			AutoYes: true,
		},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--ci")

	t.Run(
		"present ci tools are reinstalled: --ci implies --force",
		func(t *testing.T) {
			t.Parallel()
			for _, force := range []bool{false, true} {
				r := mocks.NewMockCommandRunner(gomock.NewController(t))
				r.EXPECT().Exists(gomock.Any()).AnyTimes().Return(true)
				for _, bin := range []string{"req-ci", "opt-ci"} {
					r.EXPECT().
						RunWithEnv(gomock.Any(), "/root", []string{"GOTOOLCHAIN=local"},
							"go", "install", "x/"+bin+"@v1.0.0").
						Return(nil)
				}
				require.NoError(
					t,
					toolchain.NewInstaller(r, fixtures.NopLogger(), "linux-apt", denyPrompter(t)).
						InstallTools(context.Background(), "/root", cat, types.InstallToolsOptions{
							CI:      true,
							Force:   force,
							AutoYes: true,
						}),
					"--force=%v",
					force,
				)
			}
		},
	)
}

// TestClientsToolchainInstaller_UsesInjectedPrompter tests that the installer confirms
// through the Prompter its front-end injected, not process stdin.
//
// Why this test is important:
//   - A capability served by an MCP server or a web app has no stdin; the installer
//     must ask whatever operator channel its front-end wired, and must not ask at all
//     when the caller already said yes.
//
// What it tests:
//   - Provision asks the injected Prompter once, naming the missing tool; a "no"
//     installs nothing and fails with CodeUnavailable.
//   - InstallSet with AutoYes installs without calling the Prompter.
func TestClientsToolchainInstaller_UsesInjectedPrompter(t *testing.T) {
	t.Parallel()

	t.Run("declined", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		r := mocks.NewMockCommandRunner(ctrl)
		r.EXPECT().Exists("go").Return(true)
		r.EXPECT().Exists("wire").Return(false)
		p := mocks.NewMockPrompter(ctrl)
		p.EXPECT().
			Confirm(gomock.Any(), "Install missing tools (wire)?").
			Return(false, nil)

		inst := toolchain.NewInstaller(
			r, fixtures.NopLogger(), "linux-apt", p,
		)
		err := inst.Provision(
			context.Background(),
			"/root",
			provisionCatalog(t),
			false,
			false,
		)
		require.Error(t, err)
		assert.True(t, apperr.Is(err, apperr.CodeUnavailable), err.Error())
	})

	t.Run("auto-yes skips the prompter", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		r := mocks.NewMockCommandRunner(ctrl)
		r.EXPECT().Exists("grpcurl").Return(false)
		r.EXPECT().
			RunWithEnv(gomock.Any(), "/root", gomock.Any(), "go", "install", "grpcurl@v1").
			Return(nil)
		p := mocks.NewMockPrompter(ctrl) // no EXPECT: any Confirm call fails the test

		inst := toolchain.NewInstaller(
			r, fixtures.NopLogger(), "linux-apt", p,
		)
		require.NoError(t, inst.InstallSet(
			context.Background(), "/root",
			[]types.ToolDef{{
				Name: "grpcurl", Binary: "grpcurl",
				InstallMethods: map[string]types.InstallMethod{
					"all": {Command: "go", Args: []string{"install", "grpcurl@v1"}},
				},
			}}, types.InstallToolsOptions{AutoYes: true},
		))
	})
}

// TestClientsToolchainInstaller_DenyPrompterRefuses tests the installer's confirmation
// when the front-end has no operator and injects the deny Prompter.
//
// Why this test is important:
//   - A front-end with no operator must fail loudly with the --yes guidance rather than
//     block on a stdin it can't read, or read "no" and quietly install nothing.
//
// What it tests:
//   - Provision with the deny Prompter and no AutoYes, with a required tool missing,
//     fails with CodeInvalidInput naming --yes and runs no install command.
func TestClientsToolchainInstaller_DenyPrompterRefuses(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	// No RunWithEnv EXPECT: an install fails the test.
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Exists("go").Return(true)
	r.EXPECT().Exists("wire").Return(false)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "linux-apt", denyPrompter(t))
	err := inst.Provision(
		context.Background(),
		"/root",
		provisionCatalog(t),
		false,
		false,
	)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), err.Error())
	assert.Contains(t, err.Error(), "--yes")
}

// TestClientsToolchainInstaller_NilPrompterFailsLoudly tests the installer's
// confirmation when the caller injected no Prompter.
//
// Why this test is important:
//   - The Prompter is a required collaborator the caller injects (a client never builds a
//     sibling client); a caller that forgets it must get a coded error, not a nil-pointer
//     panic at the first install that needs confirming.
//
// What it tests:
//   - Provision with a nil Prompter, without AutoYes and with a required tool missing,
//     fails with CodeInvalidInput and runs no install command.
func TestClientsToolchainInstaller_NilPrompterFailsLoudly(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	// No RunWithEnv EXPECT: an install fails the test.
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Exists("go").Return(true)
	r.EXPECT().Exists("wire").Return(false)

	inst := toolchain.NewInstaller(r, fixtures.NopLogger(), "linux-apt", nil)
	err := inst.Provision(
		context.Background(),
		"/root",
		provisionCatalog(t),
		false,
		false,
	)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), err.Error())
}

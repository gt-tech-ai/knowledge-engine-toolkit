package unit_test

import (
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

// pinnedGoTool is a CI tool installed with `go install <pkg>@<version>` everywhere.
func pinnedGoTool(name, pkgAtVersion string) types.ToolDef {
	return fixtures.Build(func(row *types.ToolDef) {
		row.Name = name
		row.Binary = name
		row.CI = true
		row.InstallMethods = map[string]types.InstallMethod{
			textAll: {
				Command:  textGoLower,
				Args:     []string{textInstall, pkgAtVersion},
				Download: nil,
			},
		}
	})
}

// TestClientsToolchainCheckPins_FailsWhenTheBinaryOnPathIsNotThePin tests the
// pinned-tool check preflight runs before formatting.
//
// Why this test is important:
//   - CI installs each tool at its manifest pin; a local gofumpt at another version
//     formats the same file differently, so a local preflight "passes" formatting that CI
//     rejects. The check must catch the drift from the binary itself, name the fix, and
//     not block on tools it can't read.
//
// What it tests (the PATH lookup and the `go version -m` subprocess are generated mocks):
//   - A binary built from the pinned package at the pinned version passes.
//   - Another version, or another package with the same binary name, fails naming both
//     builds and a forced reinstall.
//   - A binary with no Go build info is skipped; a tool not on PATH is skipped.
//   - A tool without a go-install pin is not checked at all.
func TestClientsToolchainCheckPins_FailsWhenTheBinaryOnPathIsNotThePin(t *testing.T) {
	t.Parallel()

	testClientsToolchainCheckPinsRunsCases(t)
}

// checkGofumptPin runs CheckPins over tools with path resolving gofumpt and res as its
// `go version -m` output.
func checkGofumptPin(
	t *testing.T,
	path string,
	tools []types.ToolDef,
	res *interfaces.CmdResult,
) types.StepResults {
	t.Helper()

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textRoot, textGoLower, textVersion, "-m", path).
		Return(*res)

	commandRunner.EXPECT().
		RunBuffered(gomock.Any(), textRoot, textGoLower, textEnv, textGOVERSION).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte(textGo1270 + textNewline)
		})).
		MaxTimes(1)

	paths := mocks.NewMockPathResolver(ctrl)
	paths.EXPECT().LookPath(textGofumpt).Return(path, nil)

	return toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textLinuxApt,
		denyPrompter(t),
		toolchain.WithPathResolver(paths),
		toolchain.WithReinstallHint(func(binary, _ string) string {
			return "tools install " + binary + " --force"
		}),
	).
		CheckPins(t.Context(), textRoot, tools)
}

// testClientsToolchainCheckPinsRunsCases continues
// TestClientsToolchainCheckPins_FailsWhenTheBinaryOnPathIsNotThePin.
func testClientsToolchainCheckPinsRunsCases(t *testing.T) {
	t.Helper()

	const path = textGofumptPath

	info := func(pkg, ver string) interfaces.CmdResult {
		return fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte(path + ": " + textGo1270 + "\n\tpath\t" + pkg +
				"\n\tmod\t" + pkg + "\t" + ver + "\th1:x=\n")
		})
	}
	tools := []types.ToolDef{
		pinnedGoTool(textGofumpt, "mvdan.cc/gofumpt@v0.10.0"),
		fixtures.Build(func(row *types.ToolDef) {
			row.Name = textTerraform
			row.Binary = textTerraform
			row.CI = true
		}), // no go-install pin
	}
	check := func(res interfaces.CmdResult) types.StepResults {
		t.Helper()

		return checkGofumptPin(t, path, tools, &res)
	}

	got := check(info("mvdan.cc/gofumpt", "v0.10.0"))
	require.Len(t, got, 1, "only the go-install-pinned tool is checked")
	assert.Equal(t, types.StatusPass, got[0].Status, got[0].Error)

	for name, res := range map[string]interfaces.CmdResult{
		"other version": info("mvdan.cc/gofumpt", "v0.12.0"),
		"other package": info("example.com/gofumpt", "v0.10.0"),
	} {
		got = check(res)
		require.Len(t, got, 1, name)
		assert.Equal(t, types.StatusFail, got[0].Status, name)
		assert.Contains(t, got[0].Error, "pins mvdan.cc/gofumpt@v0.10.0", name)
		assert.Contains(
			t,
			got[0].Error,
			"run `tools install gofumpt --force`",
			name,
		)
	}

	got = check(
		fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte(path + ": not a Go binary\n")
		}),
	)

	testClientsToolchainCheckPinsFailsWhenTheBinaryOnPathIsNotThePinSkipsWhenUnresolved(
		t,
		tools,
		got,
	)
}

// testClientsToolchainCheckPinsFailsWhenTheBinaryOnPathIsNotThePinSkipsWhenUnresolved
// continues TestClientsToolchainCheckPins_FailsWhenTheBinaryOnPathIsNotThePin: skips
// when unresolved.
func testClientsToolchainCheckPinsFailsWhenTheBinaryOnPathIsNotThePinSkipsWhenUnresolved(
	t *testing.T,
	tools []types.ToolDef,
	got types.StepResults,
) {
	t.Helper()

	assert.Equal(t, types.StatusSkip, got[0].Status)

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl) // strict: nothing may run
	paths := mocks.NewMockPathResolver(ctrl)
	paths.EXPECT().LookPath(textGofumpt).Return("", assert.AnError)

	got = toolchain.NewInstaller(
		r,
		fixtures.NopLogger(),
		textLinuxApt,
		denyPrompter(t),
		toolchain.WithPathResolver(paths),
	).
		CheckPins(t.Context(), textRoot, tools)
	require.Len(t, got, 1)
	assert.Equal(t, types.StatusSkip, got[0].Status)
}

// Literals for the pinned-tool checks.
const (
	// textGOVERSION is the `go env` key naming the local toolchain's version.
	textGOVERSION = "GOVERSION"

	// textGo1270 is a Go release, as `go version -m` and `go env GOVERSION` print it.
	textGo1270 = "go1.27.0"

	// textGo1274 is the local toolchain in the build-Go cases.
	textGo1274 = "go1.27.4"

	// textGofumptPath is where the pinned-tool checks find gofumpt.
	textGofumptPath = "/usr/local/bin/gofumpt"
)

// TestClientsToolchainCheckPins_FailsWhenAnOlderGoBuiltThePin tests that the pin check
// rejects a pinned binary built by an older Go than the local toolchain.
//
// Why this test is important:
//   - A `go install` binary embeds the go/types of the Go that built it. After a Go
//     upgrade, a mockgen or golines at the pinned version but built by the old Go cannot
//     load packages that need the new one ("package requires newer Go version"), and
//     every generator or formatter run fails with that instead of "reinstall the tool".
//
// What it tests (the PATH lookup and the subprocesses are generated mocks):
//   - The pinned build from an older Go minor fails, naming both Go versions and the
//     reinstall command.
//   - The same or a newer Go minor passes; an older patch alone passes.
//   - When `go env GOVERSION` fails, the pin match alone decides (pass).
func TestClientsToolchainCheckPins_FailsWhenAnOlderGoBuiltThePin(t *testing.T) {
	t.Parallel()

	check := func(builtBy string, toolchainRes interfaces.CmdResult) types.StepResult {
		t.Helper()

		ctrl := gomock.NewController(t)
		commandRunner := mocks.NewMockCommandRunner(ctrl)
		commandRunner.EXPECT().
			RunBuffered(gomock.Any(), textRoot, textGoLower, textVersion, "-m",
				textGofumptPath).
			Return(fixtures.Build(func(row *interfaces.CmdResult) {
				row.Stdout = []byte(textGofumptPath + ": " + builtBy +
					"\n\tpath\tmvdan.cc/gofumpt" +
					"\n\tmod\tmvdan.cc/gofumpt\tv0.10.0\th1:x=\n")
			}))
		commandRunner.EXPECT().
			RunBuffered(gomock.Any(), textRoot, textGoLower, textEnv, textGOVERSION).
			Return(toolchainRes)

		paths := mocks.NewMockPathResolver(ctrl)
		paths.EXPECT().LookPath(textGofumpt).Return(textGofumptPath, nil)

		got := toolchain.NewInstaller(commandRunner, fixtures.NopLogger(), textLinuxApt,
			denyPrompter(t),
			toolchain.WithPathResolver(paths),
		).
			CheckPins(t.Context(), textRoot,
				[]types.ToolDef{pinnedGoTool(textGofumpt, "mvdan.cc/gofumpt@v0.10.0")})
		require.Len(t, got, 1)

		return got[0]
	}
	goVersion := func(text string) interfaces.CmdResult {
		return fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte(text + textNewline)
		})
	}

	stale := check("go1.26.3", goVersion(textGo1274))
	assert.Equal(t, types.StatusFail, stale.Status)
	assert.Contains(t, stale.Error, "built by go1.26.3")
	assert.Contains(t, stale.Error, textGo1274)
	assert.Contains(t, stale.Error, "run `go install mvdan.cc/gofumpt@v0.10.0`")

	for _, builtBy := range []string{textGo1270, "go1.28.1"} {
		got := check(builtBy, goVersion(textGo1274))
		assert.Equal(t, types.StatusPass, got.Status, builtBy+": "+got.Error)
	}

	failed := fixtures.Build(func(row *interfaces.CmdResult) { row.Err = assert.AnError })
	assert.Equal(t, types.StatusPass, check("go1.26.3", failed).Status)
}

// TestClientsToolchainInstalledVersion_ReadsTheBinaryOnPath tests the installed-version
// probe the step cache keys a tool by.
//
// Why this test is important:
//   - A cached tool-run pass is only valid for the tool that produced it; reinstalling
//     the tool at another version must change the key, so the version must come from
//     the binary on PATH, never from the catalog's pin.
//
// What it tests (the PATH lookup and the subprocesses are generated mocks):
//   - A Go binary is keyed by its build info's package and module version.
//   - A binary with no Go build info is keyed by its version command's output.
//   - A tool with neither, or not on PATH, is "".
func TestClientsToolchainInstalledVersion_ReadsTheBinaryOnPath(t *testing.T) {
	t.Parallel()

	const path = "/usr/local/bin/tool"

	notGo := fixtures.Build(func(row *interfaces.CmdResult) {
		row.Stdout = []byte(path + ": not a Go binary\n")
	})
	probe := func(tool types.ToolDef, info interfaces.CmdResult, version string) string {
		t.Helper()

		ctrl := gomock.NewController(t)
		commandRunner := mocks.NewMockCommandRunner(ctrl)
		commandRunner.EXPECT().
			RunBuffered(gomock.Any(), textRoot, textGoLower, textVersion, "-m", path).
			Return(info)

		if len(tool.VersionCmd) > 0 && version != "" {
			commandRunner.EXPECT().
				RunBuffered(gomock.Any(), textRoot, tool.VersionCmd[0], tool.VersionCmd[1:]).
				Return(fixtures.Build(func(row *interfaces.CmdResult) {
					row.Stdout = []byte(version + textNewline)
				}))
		}

		paths := mocks.NewMockPathResolver(ctrl)
		paths.EXPECT().LookPath(tool.Binary).Return(path, nil)

		return toolchain.NewInstaller(commandRunner, fixtures.NopLogger(), textLinuxApt,
			denyPrompter(t),
			toolchain.WithPathResolver(paths),
		).
			InstalledVersion(t.Context(), textRoot, &tool)
	}
	goInfo := fixtures.Build(func(row *interfaces.CmdResult) {
		row.Stdout = []byte(path + ": go1.27.0\n\tpath\tmvdan.cc/gofumpt" +
			"\n\tmod\tmvdan.cc/gofumpt\tv0.10.0\th1:x=\n")
	})

	testClientsToolchainInstalledVersionReadsTheBinaryOnPathChecksProbes(t,
		&notGo,
		probe,
		&goInfo,
	)
}

// testClientsToolchainInstalledVersionReadsTheBinaryOnPathChecksProbes continues
// TestClientsToolchainInstalledVersion_ReadsTheBinaryOnPath.
func testClientsToolchainInstalledVersionReadsTheBinaryOnPathChecksProbes(t *testing.T,
	notGo *interfaces.CmdResult,
	probe func(tool types.ToolDef, info interfaces.CmdResult, version string) string,
	goInfo *interfaces.CmdResult,
) {
	t.Helper()

	assert.Equal(t, "mvdan.cc/gofumpt@v0.10.0",
		probe(pinnedGoTool(textTool, "mvdan.cc/gofumpt@v0.9.0"), *goInfo, ""))

	shellcheck := fixtures.Build(func(row *types.ToolDef) {
		row.Name = textTool
		row.Binary = textTool
		row.VersionCmd = []string{textTool, textVersionFlag}
	})
	assert.Equal(t, "ShellCheck 0.10.0", probe(shellcheck, *notGo, "ShellCheck 0.10.0"))
	assert.Empty(
		t,
		probe(
			fixtures.Build(func(row *types.ToolDef) {
				row.Name = textTool
				row.Binary = textTool
			}),
			*notGo,
			"",
		),
	)

	ctrl := gomock.NewController(t)
	paths := mocks.NewMockPathResolver(ctrl)
	paths.EXPECT().LookPath(textTool).Return("", assert.AnError)
	assert.Empty(
		t,
		toolchain.NewInstaller(mocks.NewMockCommandRunner(ctrl), fixtures.NopLogger(),
			textLinuxApt, denyPrompter(t), toolchain.WithPathResolver(paths)).
			InstalledVersion(t.Context(), textRoot, &shellcheck),
	)
}

// TestClientsToolchainProvision_ReinstallsADriftedPin tests that setup reinstalls a
// required go-install tool whose binary on PATH is not the pinned build.
//
// Why this test is important:
//   - The detector only asks whether the binary exists and meets a minimum, so a stale
//     go-install binary (another pin, or built by an older Go) scanned as OK and setup
//     kept it; the developer then hit the pin check or a "requires newer Go version"
//     failure later, with no setup run that would fix it.
//
// What it tests (the PATH lookup and the subprocesses are generated mocks):
//   - A required tool on PATH at another version is reinstalled at its pin and
//     verified, and Provision succeeds.
func TestClientsToolchainProvision_ReinstallsADriftedPin(t *testing.T) {
	t.Setenv(textGobin, t.TempDir())

	const wirePath = "/usr/local/bin/wire"

	ctrl := gomock.NewController(t)
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Exists(textGoLower).Return(true)
	gomock.InOrder(
		commandRunner.EXPECT().Exists(textWire).Return(true),
		commandRunner.EXPECT().
			RunBuffered(gomock.Any(), textRoot, textGoLower, textVersion, "-m", wirePath).
			Return(fixtures.Build(func(row *interfaces.CmdResult) {
				row.Stdout = []byte(wirePath + ": " + textGo1270 +
					"\n\tpath\tx/wire\n\tmod\tx/wire\tv0\th1:x=\n")
			})),
		commandRunner.EXPECT().RunWithEnv(
			gomock.Any(), textRoot, []string{textGotoolchainLocal},
			textGoLower, textInstall, "x/wire@v1",
		).Return(nil),
		commandRunner.EXPECT().Exists(textWire).Return(true),
	)

	paths := mocks.NewMockPathResolver(ctrl)
	paths.EXPECT().LookPath(textWire).Return(wirePath, nil)

	err := toolchain.NewInstaller(commandRunner, fixtures.NopLogger(), textLinuxApt,
		denyPrompter(t),
		toolchain.WithPathResolver(paths),
	).
		Provision(t.Context(), textRoot, provisionCatalog(t),
			fixtures.BuildRef(func(row *types.InstallToolsOptions) {
				row.AutoYes = true
			}))
	require.NoError(t, err)
}

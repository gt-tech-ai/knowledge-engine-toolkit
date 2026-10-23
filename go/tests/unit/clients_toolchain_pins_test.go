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
	return types.ToolDef{
		Name: name, Binary: name, CI: true,
		InstallMethods: map[string]types.InstallMethod{
			"all": {Command: "go", Args: []string{"install", pkgAtVersion}},
		},
	}
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
	const path = "/usr/local/bin/gofumpt"
	info := func(pkg, ver string) interfaces.CmdResult {
		return interfaces.CmdResult{Stdout: []byte(path + ": go1.27.0\n\tpath\t" + pkg +
			"\n\tmod\t" + pkg + "\t" + ver + "\th1:x=\n")}
	}
	tools := []types.ToolDef{
		pinnedGoTool("gofumpt", "mvdan.cc/gofumpt@v0.10.0"),
		{Name: "terraform", Binary: "terraform", CI: true}, // no go-install pin
	}
	check := func(res interfaces.CmdResult) types.StepResults {
		t.Helper()
		ctrl := gomock.NewController(t)
		r := mocks.NewMockCommandRunner(ctrl)
		r.EXPECT().
			RunBuffered(gomock.Any(), "/root", "go", "version", "-m", path).
			Return(res)
		paths := mocks.NewMockPathResolver(ctrl)
		paths.EXPECT().LookPath("gofumpt").Return(path, nil)
		return toolchain.NewInstaller(
			r,
			fixtures.NopLogger(),
			"linux-apt",
			denyPrompter(t),
			toolchain.WithPathResolver(paths),
			toolchain.WithReinstallHint(func(binary, _ string) string {
				return "tools install " + binary + " --force"
			}),
		).
			CheckPins(t.Context(), "/root", tools)
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

	got = check(interfaces.CmdResult{Stdout: []byte(path + ": not a Go binary\n")})
	assert.Equal(t, types.StatusSkip, got[0].Status)

	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl) // strict: nothing may run
	paths := mocks.NewMockPathResolver(ctrl)
	paths.EXPECT().LookPath("gofumpt").Return("", assert.AnError)
	got = toolchain.NewInstaller(
		r,
		fixtures.NopLogger(),
		"linux-apt",
		denyPrompter(t),
		toolchain.WithPathResolver(paths),
	).
		CheckPins(t.Context(), "/root", tools)
	require.Len(t, got, 1)
	assert.Equal(t, types.StatusSkip, got[0].Status)
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
	const path = "/usr/local/bin/tool"
	notGo := interfaces.CmdResult{Stdout: []byte(path + ": not a Go binary\n")}
	probe := func(tool types.ToolDef, info interfaces.CmdResult, version string) string {
		t.Helper()
		ctrl := gomock.NewController(t)
		r := mocks.NewMockCommandRunner(ctrl)
		r.EXPECT().
			RunBuffered(gomock.Any(), "/root", "go", "version", "-m", path).
			Return(info)
		if len(tool.VersionCmd) > 0 && version != "" {
			r.EXPECT().
				RunBuffered(gomock.Any(), "/root", tool.VersionCmd[0], tool.VersionCmd[1:]).
				Return(interfaces.CmdResult{Stdout: []byte(version + "\n")})
		}
		paths := mocks.NewMockPathResolver(ctrl)
		paths.EXPECT().LookPath(tool.Binary).Return(path, nil)
		return toolchain.NewInstaller(r, fixtures.NopLogger(), "linux-apt", denyPrompter(t),
			toolchain.WithPathResolver(paths),
		).
			InstalledVersion(t.Context(), "/root", &tool)
	}
	goInfo := interfaces.CmdResult{
		Stdout: []byte(path + ": go1.27.0\n\tpath\tmvdan.cc/gofumpt" +
			"\n\tmod\tmvdan.cc/gofumpt\tv0.10.0\th1:x=\n"),
	}

	assert.Equal(t, "mvdan.cc/gofumpt@v0.10.0",
		probe(pinnedGoTool("tool", "mvdan.cc/gofumpt@v0.9.0"), goInfo, ""))
	shellcheck := types.ToolDef{
		Name:       "tool",
		Binary:     "tool",
		VersionCmd: []string{"tool", "--version"},
	}
	assert.Equal(t, "ShellCheck 0.10.0", probe(shellcheck, notGo, "ShellCheck 0.10.0"))
	assert.Empty(t, probe(types.ToolDef{Name: "tool", Binary: "tool"}, notGo, ""))

	ctrl := gomock.NewController(t)
	paths := mocks.NewMockPathResolver(ctrl)
	paths.EXPECT().LookPath("tool").Return("", assert.AnError)
	assert.Empty(
		t,
		toolchain.NewInstaller(mocks.NewMockCommandRunner(ctrl), fixtures.NopLogger(),
			"linux-apt", denyPrompter(t), toolchain.WithPathResolver(paths)).
			InstalledVersion(t.Context(), "/root", &shellcheck),
	)
}

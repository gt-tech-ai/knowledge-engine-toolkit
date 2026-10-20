package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/toolchain"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestClientsToolchainInstaller_Install_WingetAddsNonInteractiveFlags verifies the
// winget branch appends the non-interactive source/agreement flags.
//
// Why this test is important:
//   - A winget install missing --accept-source-agreements/--accept-package-agreements
//     blocks on an interactive prompt, hanging non-interactive setup runs.
//
// What it tests:
//   - A winget method routes through the runner with the two accept flags and the
//     explicit --source winget appended after the method's own args.
func TestClientsToolchainInstaller_Install_WingetAddsNonInteractiveFlags(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)
	r.EXPECT().Run(
		gomock.Any(), "/root", "winget",
		"install", "Foo.Bar",
		"--source", "winget",
		"--accept-source-agreements", "--accept-package-agreements",
	).Return(nil)

	tool := &types.ToolDef{
		Name: "foo",
		InstallMethods: map[string]types.InstallMethod{
			"windows": {Command: "winget", Args: []string{"install", "Foo.Bar"}},
		},
	}
	inst := toolchain.NewInstaller(
		r,
		fixtures.NopLogger(),
		"windows",
		denyPrompter(t),
		toolchain.WithArch("amd64"),
	)
	require.NoError(t, inst.Install(context.Background(), "/root", tool))
}

// TestClientsToolchainInstaller_Install_WindowsArm64CrossCompiles verifies the
// Windows-ARM64 go-install cross-compile rewrite, exercised through the injected arch.
//
// Why this test is important:
//   - Go tools don't ship native windows/arm64 builds, so the installer rewrites
//     go-install to an amd64 `-p 1` cross-compile; a wrong rewrite produces a
//     binary for the wrong architecture. Injecting the arch makes this branch
//     deterministic without a matching host.
//
// What it tests:
//   - On windows+arm64 a go-install becomes `go install -p 1 <pkg>` with
//     GOTOOLCHAIN=local, GOARCH=amd64, and CGO_ENABLED=0 in the environment.
func TestClientsToolchainInstaller_Install_WindowsArm64CrossCompiles(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl)

	var gotEnv, gotArgs []string
	r.EXPECT().RunWithEnv(gomock.Any(), "/root", gomock.Any(), "go", gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			_ string,
			env []string,
			_ string,
			args ...string,
		) error {
			gotEnv, gotArgs = env, args
			return nil
		})

	tool := &types.ToolDef{
		Name: "x",
		InstallMethods: map[string]types.InstallMethod{
			"all": {Command: "go", Args: []string{"install", "pkg@v1"}},
		},
	}
	inst := toolchain.NewInstaller(
		r,
		fixtures.NopLogger(),
		"windows",
		denyPrompter(t),
		toolchain.WithArch("arm64"),
	)
	require.NoError(t, inst.Install(context.Background(), "/root", tool))

	assert.Equal(t, []string{"install", "-p", "1", "pkg@v1"}, gotArgs)
	assert.Contains(t, gotEnv, "GOTOOLCHAIN=local")
	assert.Contains(t, gotEnv, "GOARCH=amd64")
	assert.Contains(t, gotEnv, "CGO_ENABLED=0")
}

// TestClientsToolchainInstaller_Install_WindowsPrefersGoInstall verifies method
// resolution favors a cross-platform go-install over a native Windows installer.
//
// Why this test is important:
//   - On Windows the native installers (winget/powershell) are fragile, so a
//     go-install "all" method is preferred when present; picking the native path
//     instead means Windows setup takes the flaky route.
//
// What it tests:
//   - With both a "windows" (powershell) and an "all" (go-install) method, Install
//     runs the go-install (via RunWithEnv), never the powershell command — a strict
//     mock with no powershell expectation fails if the native path is taken.
func TestClientsToolchainInstaller_Install_WindowsPrefersGoInstall(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	r := mocks.NewMockCommandRunner(ctrl) // strict: only the go-install call is allowed
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
		Name: "x",
		InstallMethods: map[string]types.InstallMethod{
			"windows": {Command: "powershell", Args: []string{"-c", "x"}},
			"all":     {Command: "go", Args: []string{"install", "pkg@v1"}},
		},
	}
	inst := toolchain.NewInstaller(
		r,
		fixtures.NopLogger(),
		"windows",
		denyPrompter(t),
		toolchain.WithArch("amd64"),
	)
	require.NoError(t, inst.Install(context.Background(), "/root", tool))
}

// TestClientsToolchainClassifyWingetExit maps the winget HRESULT exit codes.
//
// Why this test is important:
//   - winget returns non-zero HRESULT codes for "already installed" states;
//     misclassifying them turns a successful no-op into a setup failure (or hides
//     a real failure as success).
//
// What it tests:
//   - Each known "already installed" HRESULT is success-like with its description.
//   - Zero, generic, and other HRESULT codes are not success-like.
func TestClientsToolchainClassifyWingetExit(t *testing.T) {
	t.Parallel()

	success := map[int]string{
		int(uint32(0x8A15002B)): "already installed, no update available",
		int(uint32(0x8A150061)): "package already installed",
		int(uint32(0x8A15010D)): "another version already installed",
		int(uint32(0x8A15004F)): "upgrade version not newer than installed",
	}
	for code, want := range success {
		desc, ok := toolchain.ClassifyWingetExit(code)
		assert.Truef(t, ok, "code %#x should be success-like", code)
		assert.Equal(t, want, desc)
	}

	for _, code := range []int{0, 1, int(uint32(0x8A150001)), int(uint32(0x8A150008))} {
		_, ok := toolchain.ClassifyWingetExit(code)
		assert.Falsef(t, ok, "code %#x is not success-like", code)
	}
}

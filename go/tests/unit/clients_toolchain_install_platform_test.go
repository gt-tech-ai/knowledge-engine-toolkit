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
	commandRunner := mocks.NewMockCommandRunner(ctrl)
	commandRunner.EXPECT().Run(
		gomock.Any(), textRoot, "winget",
		textInstall, "Foo.Bar",
		"--source", "winget",
		"--accept-source-agreements", "--accept-package-agreements",
	).Return(nil)

	tool := fixtures.BuildRef(func(row *types.ToolDef) {
		row.Name = "foo"
		row.InstallMethods = map[string]types.InstallMethod{
			textWindows: {
				Command:  "winget",
				Args:     []string{textInstall, "Foo.Bar"},
				Download: nil,
			},
		}
	})
	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textWindows,
		denyPrompter(t),
		toolchain.WithArch("amd64"),
	)
	require.NoError(t, inst.Install(context.Background(), textRoot, tool))
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
	commandRunner := mocks.NewMockCommandRunner(ctrl)

	var gotEnv, gotArgs []string

	commandRunner.EXPECT().
		RunWithEnv(gomock.Any(), textRoot, gomock.Any(), textGoLower, gomock.Any()).
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

	tool := fixtures.BuildRef(func(row *types.ToolDef) {
		row.Name = textX
		row.InstallMethods = map[string]types.InstallMethod{
			textAll: {
				Command:  textGoLower,
				Args:     []string{textInstall, textPkgV1},
				Download: nil,
			},
		}
	})
	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textWindows,
		denyPrompter(t),
		toolchain.WithArch("arm64"),
	)
	require.NoError(t, inst.Install(context.Background(), textRoot, tool))

	assert.Equal(t, []string{textInstall, "-p", "1", textPkgV1}, gotArgs)
	assert.Contains(t, gotEnv, textGotoolchainLocal)
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
	commandRunner := mocks.NewMockCommandRunner(
		ctrl,
	) // strict: only the go-install call is allowed
	commandRunner.EXPECT().
		RunWithEnv(
			gomock.Any(),
			textRoot,
			[]string{textGotoolchainLocal},
			textGoLower,
			textInstall,
			textPkgV1,
		).
		Return(nil)

	tool := fixtures.BuildRef(func(row *types.ToolDef) {
		row.Name = textX
		row.InstallMethods = map[string]types.InstallMethod{
			textWindows: {
				Command:  textPowershell,
				Args:     []string{"-c", textX},
				Download: nil,
			},
			textAll: {
				Command:  textGoLower,
				Args:     []string{textInstall, textPkgV1},
				Download: nil,
			},
		}
	})
	inst := toolchain.NewInstaller(
		commandRunner,
		fixtures.NopLogger(),
		textWindows,
		denyPrompter(t),
		toolchain.WithArch("amd64"),
	)
	require.NoError(t, inst.Install(context.Background(), textRoot, tool))
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
		int(uint32(uint32Arg0x8A15002B)): "already installed, no update available",
		int(uint32(uint32Arg0x8A)):       "package already installed",
		int(uint32(uint32Arg0x8A15010D)): "another version already installed",
		int(uint32(uint32Arg0x8A15004F)): "upgrade version not newer than installed",
	}
	for code, want := range success {
		desc, ok := toolchain.ClassifyWingetExit(code)
		assert.Truef(t, ok, "code %#x should be success-like", code)
		assert.Equal(t, want, desc)
	}

	for _, code := range []int{
		0, 1, int(uint32(uint32Arg0x8AExit)),
		int(uint32(uint32Arg0x8AWingetExit)),
	} {
		_, ok := toolchain.ClassifyWingetExit(code)
		assert.Falsef(t, ok, "code %#x is not success-like", code)
	}
}

const (
	// uint32Arg0x8A is argument 1 of uint32 in TestClientsToolchainClassifyWingetExit.
	uint32Arg0x8A = 0x8A150061

	// uint32Arg0x8AExit is argument 1 of uint32 in
	// TestClientsToolchainClassifyWingetExit.
	uint32Arg0x8AExit = 0x8A150001

	// uint32Arg0x8AWingetExit is argument 1 of uint32 in
	// TestClientsToolchainClassifyWingetExit.
	uint32Arg0x8AWingetExit = 0x8A150008
)

const (
	// uint32Arg0x8A15002B is the uint32Arg value 0x8A15002B.
	uint32Arg0x8A15002B = 0x8A15002B

	// uint32Arg0x8A15004F is the uint32Arg value 0x8A15004F.
	uint32Arg0x8A15004F = 0x8A15004F

	// uint32Arg0x8A15010D is the uint32Arg value 0x8A15010D.
	uint32Arg0x8A15010D = 0x8A15010D
)

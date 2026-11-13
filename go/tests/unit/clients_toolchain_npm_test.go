package unit_test

import (
	"os"
	"path/filepath"
	"slices"
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

// Literals for TestClientsToolchainInstaller_Install_NpmFallsBackToUserPrefix.
const (
	// textNpm is the npm command.
	textNpm = "npm"

	// textNpmGlobal is npm's global-install flag.
	textNpmGlobal = "-g"

	// textCspellPin is an npm package pinned to a version.
	textCspellPin = "cspell@10.3.4"

	// readOnlyDirMode is a directory mode its owner can read and enter but not write.
	readOnlyDirMode = 0o500

	// writableDirMode is a directory mode its owner can read, enter and write.
	writableDirMode = 0o700
)

// TestClientsToolchainInstaller_Install_NpmFallsBackToUserPrefix tests that an
// `npm install -g` installs under ~/.local when npm's global prefix is not writable.
//
// Why this test is important:
//   - The Node.js installer from nodejs.org leaves the global prefix (/usr/local)
//     owned by root, so `npm install -g` fails with EACCES and setup stops on a tool
//     the developer could install for themselves; running npm under sudo is worse.
//   - A writable prefix (Homebrew's Node) must keep the plain command, so the
//     fallback never moves a tool that installs fine today.
//
// What it tests:
//   - With a read-only <prefix>/lib/node_modules, the install gains
//     `--prefix ~/.local`, ~/.local/bin goes on PATH, and a warning says to add it
//     to the shell profile.
//   - With a writable prefix, the command is unchanged and PATH is untouched.
func TestClientsToolchainInstaller_Install_NpmFallsBackToUserPrefix(t *testing.T) {
	cases := []npmPrefixCase{
		{name: "admin-owned prefix", modulesMode: readOnlyDirMode, fallback: true},
		{name: "writable prefix", modulesMode: writableDirMode, fallback: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv(textPath, textUsrBin)

			spy := fixtures.NewSpyLogger()
			inst := toolchain.NewInstaller(
				npmRunner(t, testCase, filepath.Join(home, ".local")),
				spy, textLinuxApt, denyPrompter(t))

			require.NoError(t, inst.Install(t.Context(), textRoot, npmTool()))

			userBin := filepath.Join(home, ".local", textBin)
			onPath := slices.Contains(filepath.SplitList(os.Getenv(textPath)), userBin)
			assert.Equal(t, testCase.fallback, onPath, "PATH %q", os.Getenv(textPath))
			assert.Equal(t, testCase.fallback, len(spy.WarnCalls) > 0,
				"warnings %q", spy.WarnCalls)
		})
	}
}

// npmPrefixCase is one npm global prefix a test installs against.
type npmPrefixCase struct {
	// name labels the subtest.
	name string

	// modulesMode is the mode of the prefix's lib/node_modules.
	modulesMode os.FileMode

	// fallback is whether the install is expected to move to the user prefix.
	fallback bool
}

// npmRunner expects `npm prefix -g` to report a prefix whose lib/node_modules has the
// case's mode, then the install command: with `--prefix userPrefix` when the case
// expects the fallback.
func npmRunner(
	t *testing.T,
	testCase npmPrefixCase,
	userPrefix string,
) *mocks.MockCommandRunner {
	t.Helper()

	prefix := t.TempDir()
	modules := filepath.Join(prefix, "lib", "node_modules")
	require.NoError(t, os.MkdirAll(modules, writableDirMode))
	// The directory stays empty, so the temp-dir cleanup removes it even read-only.
	require.NoError(t, os.Chmod(modules, testCase.modulesMode))

	want := []string{textInstall, textNpmGlobal, textCspellPin}
	if testCase.fallback {
		want = append(want, "--prefix", userPrefix)
	}

	runner := mocks.NewMockCommandRunner(gomock.NewController(t))
	runner.EXPECT().
		RunBuffered(gomock.Any(), textRoot, textNpm, "prefix", textNpmGlobal).
		Return(fixtures.Build(func(row *interfaces.CmdResult) {
			row.Stdout = []byte(prefix + "\n")
		}))
	runner.EXPECT().
		RunWithEnv(gomock.Any(), textRoot, gomock.Any(), textNpm, toAny(want)...).
		Return(nil)

	return runner
}

// npmTool is a tool installed with `npm install -g cspell@10.3.4` on every platform.
func npmTool() *types.ToolDef {
	return fixtures.BuildRef(func(row *types.ToolDef) {
		row.Name = "cspell"
		row.Binary = "cspell"
		row.InstallMethods = map[string]types.InstallMethod{
			textAll: {
				Command:  textNpm,
				Args:     []string{textInstall, textNpmGlobal, textCspellPin},
				Download: nil,
			},
		}
	})
}

package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// textNpm is the npm command.
const textNpm = "npm"

// isNpmGlobalInstall reports whether a method is an `npm install -g` invocation.
func isNpmGlobalInstall(m types.InstallMethod) bool {
	return m.Command == textNpm && len(m.Args) > 0 && m.Args[0] == textInstall &&
		(slices.Contains(m.Args, "-g") || slices.Contains(m.Args, "--global"))
}

// npmMethod returns method unchanged unless it is an `npm install -g` whose global
// prefix this user cannot write (the nodejs.org installer leaves /usr/local root-owned,
// so npm fails with EACCES). Then it installs under ~/.local instead, puts ~/.local/bin
// on the process PATH, and warns when the shell PATH lacks it. Windows is unchanged: its
// default prefix is per-user.
func (i *Installer) npmMethod(
	ctx context.Context,
	root string,
	method types.InstallMethod,
) types.InstallMethod {
	if i.platform == osWindows || !isNpmGlobalInstall(method) {
		return method
	}

	res := i.runner.RunBuffered(ctx, root, textNpm, "prefix", "-g")

	prefix := strings.TrimSpace(string(res.Stdout))
	if res.Err != nil || prefix == "" ||
		writable(filepath.Join(prefix, "lib", "node_modules")) {
		return method
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return method
	}

	userPrefix := filepath.Join(home, ".local")
	userBin := filepath.Join(userPrefix, "bin")

	if !onPath(userBin) {
		i.log.Warn("npm's global prefix is not writable; installing under the user "+
			"prefix. Add its bin dir to PATH in your shell profile",
			"prefix", prefix, logKeyDir, userBin)
	}

	AddToPathIfMissing(userBin, i.log)

	method.Args = append(slices.Clone(method.Args), "--prefix", userPrefix)

	return method
}

// writable reports whether this user can create a file in dir, or, when dir does not
// exist yet, in its nearest existing ancestor (where npm would create it).
func writable(dir string) bool {
	for {
		_, err := os.Stat(dir)
		if err == nil {
			break
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}

		dir = parent
	}

	probe, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return false
	}

	name := probe.Name()

	_ = probe.Close()
	_ = os.Remove(name)

	return true
}

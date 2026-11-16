package toolchain

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Environment reads and writes the environment variables the Installer resolves and
// extends: PATH, GOBIN, GOPATH, LOCALAPPDATA and GOTOOLCHAIN. Setup extends PATH so a
// later step in the same process finds a fresh install, so the default is the process
// environment; a caller that must not change its own (a test running beside others,
// or a front-end serving several runs) injects its own with WithEnvironment. The
// PathResolver keeps reading the process PATH.
//
// SDK seam — wraps os.Getenv/os.Setenv; unit tests inject a generated mock.
type Environment interface {
	// Getenv returns the variable's value, or "" when it is unset.
	Getenv(key string) string

	// Setenv sets the variable to value.
	Setenv(key, value string) error
}

// WithEnvironment overrides the environment the Installer reads GOBIN, GOPATH and
// LOCALAPPDATA from and writes PATH and GOTOOLCHAIN to (default: the process
// environment).
func WithEnvironment(env Environment) Option {
	return func(i *Installer) { i.env = env }
}

// processEnvironment is the production Environment: the process's own variables.
type processEnvironment struct{}

// Getenv returns the process variable's value.
func (processEnvironment) Getenv(key string) string {
	return os.Getenv(key)
}

// Setenv sets the process variable.
func (processEnvironment) Setenv(key, value string) error {
	return apperr.Wrap(os.Setenv(key, value), apperr.CodeInternal, "set "+key)
}

// AddToPathIfMissing prepends dir to the process PATH when it is not already
// present (case-insensitive). Idempotent: a dir already on PATH is a no-op. An
// Installer extends its own Environment instead (WithEnvironment).
func AddToPathIfMissing(dir string, log interfaces.Logger) {
	prependIfMissing(processEnvironment{}, dir, log)
}

// prependIfMissing prepends dir to env's PATH when it is not already present
// (case-insensitive).
func prependIfMissing(env Environment, dir string, log interfaces.Logger) {
	if onPath(env, dir) {
		log.Debug("PATH entry already present", logKeyDir, dir)

		return
	}

	log.Info("Adding to PATH", logKeyDir, dir)

	setPath(env, dir+string(os.PathListSeparator)+env.Getenv(envPath), log)
}

// setPath writes value to env's PATH, warning when the write fails: the install it
// follows has succeeded, and the post-install verify reports a tool it cannot find.
func setPath(env Environment, value string, log interfaces.Logger) {
	err := env.Setenv(envPath, value)
	if err != nil {
		log.Warn("Could not update PATH", "error", err)
	}
}

// onPath reports whether dir is an entry of env's PATH (case-insensitive).
func onPath(env Environment, dir string) bool {
	return slices.ContainsFunc(
		filepath.SplitList(env.Getenv(envPath)),
		func(p string) bool {
			return strings.EqualFold(p, dir)
		},
	)
}

// moveToPathFront puts dir first on env's PATH, dropping any later copy, so
// the binaries in it win every lookup. When dir was not already first, it warns about
// each binary in dir an earlier PATH directory also holds: setup fixes only its own
// process, and the user's shell profile keeps the shadowing order.
func moveToPathFront(env Environment, dir string, log interfaces.Logger) {
	entries := filepath.SplitList(env.Getenv(envPath))
	if len(entries) > 0 && strings.EqualFold(entries[0], dir) {
		return
	}

	kept := []string{dir}
	ahead := len(entries)

	for j, entry := range entries {
		if strings.EqualFold(entry, dir) {
			ahead = min(ahead, j)

			continue
		}

		kept = append(kept, entry)
	}

	if shadowed := shadowedBinaries(dir, entries[:ahead]); len(shadowed) > 0 {
		log.Warn("Binaries in this dir are shadowed by earlier PATH entries; "+
			"put it first on PATH in your shell profile",
			logKeyDir, dir, "shadowed", strings.Join(shadowed, textCommaSpace))
	}

	log.Info("Moving to the front of PATH", logKeyDir, dir)

	setPath(env, strings.Join(kept, string(os.PathListSeparator)), log)
}

// shadowedBinaries lists each file in dir that one of the earlier directories also
// holds, as "<name> (<earlier dir>)", naming the first such directory, sorted.
func shadowedBinaries(dir string, earlier []string) []string {
	pending := make(map[string]bool)
	for _, name := range regularFiles(dir) {
		pending[name] = true
	}

	var shadowed []string

	for _, other := range earlier {
		for _, name := range regularFiles(other) {
			if pending[name] {
				shadowed = append(shadowed, name+" ("+other+")")
				delete(pending, name)
			}
		}
	}

	slices.Sort(shadowed)

	return shadowed
}

// regularFiles returns the names of the non-directory entries in dir; none when dir
// cannot be read.
func regularFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var names []string

	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	return names
}

// resolveGoBin returns the directory where `go install` places binaries: env's GOBIN
// if set, else GOPATH/bin, else ~/go/bin. Empty when the home dir can't resolve.
func resolveGoBin(env Environment) string {
	if gobin := env.Getenv("GOBIN"); gobin != "" {
		return gobin
	}

	gopath := env.Getenv("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}

		gopath = filepath.Join(home, "go")
	}

	return filepath.Join(gopath, "bin")
}

// ensureGoBinInPath puts the go-install bin dir first on the Installer's PATH, so a
// pinned tool there wins over another build earlier on PATH, and adds (on
// Windows-ARM64) its cross-compiled windows_amd64 subdir.
func (i *Installer) ensureGoBinInPath() {
	gobin := resolveGoBin(i.env)
	if gobin == "" {
		return
	}

	moveToPathFront(i.env, gobin, i.log)

	if i.platform == osWindows && i.arch == archARM64 {
		prependIfMissing(i.env, filepath.Join(gobin, "windows_amd64"), i.log)
	}
}

// ensureMsys2InPath adds the MSYS2 ucrt64 bin directory to PATH when it exists
// (the home of mingw-w64 gcc on Windows).
func (i *Installer) ensureMsys2InPath() {
	msys2Bin := `C:\msys64\ucrt64\bin`
	info, err := os.Stat(msys2Bin)

	if err == nil && info.IsDir() {
		prependIfMissing(i.env, msys2Bin, i.log)
	}
}

// addInstallDirToPath adds the directory a just-installed tool's binary lands in
// to the Installer's PATH, so the post-install verify can find it. go-install and
// pinned downloads both land in the go-install bin dir (GOBIN).
func (i *Installer) addInstallDirToPath(tool *types.ToolDef) {
	method, ok := i.resolveMethod(tool)
	if !ok {
		return
	}

	switch {
	case isGoInstall(method) || method.Download != nil:
		i.ensureGoBinInPath()
	case method.Command == "powershell" && i.platform == osWindows:
		i.addPowershellToolDir(tool)
	default:
		// No other value changes the outcome.
	}
}

// addPowershellToolDir adds where a PowerShell installer put the tool.
func (i *Installer) addPowershellToolDir(tool *types.ToolDef) {
	if tool.Binary == "gcc" {
		i.ensureMsys2InPath()

		return
	}

	if localAppData := i.env.Getenv("LOCALAPPDATA"); localAppData != "" {
		prependIfMissing(i.env, filepath.Join(localAppData, "Programs", tool.Binary),
			i.log)
	}
}

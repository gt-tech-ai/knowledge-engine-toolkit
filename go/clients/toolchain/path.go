package toolchain

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// AddToPathIfMissing prepends dir to the process PATH when it is not already
// present (case-insensitive). Idempotent: a dir already on PATH is a no-op.
func AddToPathIfMissing(dir string, log interfaces.Logger) {
	pathEnv := os.Getenv(envPath)
	for _, p := range filepath.SplitList(pathEnv) {
		if strings.EqualFold(p, dir) {
			log.Debug("PATH entry already present", logKeyDir, dir)

			return
		}
	}

	log.Info("Adding to PATH", logKeyDir, dir)

	_ = os.Setenv(envPath, dir+string(os.PathListSeparator)+pathEnv)
}

// moveToPathFront puts dir first on the process PATH, dropping any later copy, so
// the binaries in it win every lookup. When dir was not already first, it warns about
// each binary in dir an earlier PATH directory also holds: setup fixes only its own
// process, and the user's shell profile keeps the shadowing order.
func moveToPathFront(dir string, log interfaces.Logger) {
	entries := filepath.SplitList(os.Getenv(envPath))
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

	_ = os.Setenv(envPath, strings.Join(kept, string(os.PathListSeparator)))
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

// resolveGoBin returns the directory where `go install` places binaries: GOBIN
// if set, else GOPATH/bin, else ~/go/bin. Empty when the home dir can't resolve.
func resolveGoBin() string {
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		return gobin
	}

	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}

		gopath = filepath.Join(home, "go")
	}

	return filepath.Join(gopath, "bin")
}

// ensureGoBinInPath puts the go-install bin dir first on the process PATH, so a
// pinned tool there wins over another build earlier on PATH, and adds (on
// Windows-ARM64) its cross-compiled windows_amd64 subdir.
func (i *Installer) ensureGoBinInPath() {
	gobin := resolveGoBin()
	if gobin == "" {
		return
	}

	moveToPathFront(gobin, i.log)

	if i.platform == osWindows && i.arch == archARM64 {
		AddToPathIfMissing(filepath.Join(gobin, "windows_amd64"), i.log)
	}
}

// ensureMsys2InPath adds the MSYS2 ucrt64 bin directory to PATH when it exists
// (the home of mingw-w64 gcc on Windows).
func (i *Installer) ensureMsys2InPath() {
	msys2Bin := `C:\msys64\ucrt64\bin`
	info, err := os.Stat(msys2Bin)

	if err == nil && info.IsDir() {
		AddToPathIfMissing(msys2Bin, i.log)
	}
}

// addInstallDirToPath adds the directory a just-installed tool's binary lands in
// to the process PATH, so the post-install verify can find it. go-install and
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

	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		AddToPathIfMissing(filepath.Join(localAppData, "Programs", tool.Binary), i.log)
	}
}

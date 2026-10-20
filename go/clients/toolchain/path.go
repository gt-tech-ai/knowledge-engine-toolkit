package toolchain

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// AddToPathIfMissing prepends dir to the process PATH when it is not already
// present (case-insensitive). Idempotent: a dir already on PATH is a no-op.
func AddToPathIfMissing(dir string, log interfaces.Logger) {
	pathEnv := os.Getenv("PATH")
	for _, p := range filepath.SplitList(pathEnv) {
		if strings.EqualFold(p, dir) {
			log.Debug("PATH entry already present", "dir", dir)
			return
		}
	}
	log.Info("Adding to PATH", "dir", dir)
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+pathEnv)
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

// ensureGoBinInPath adds the go-install bin dir (and, on Windows-ARM64, its
// cross-compiled windows_amd64 subdir) to the process PATH.
func (i *Installer) ensureGoBinInPath() {
	gobin := resolveGoBin()
	if gobin == "" {
		return
	}
	AddToPathIfMissing(gobin, i.log)
	if i.platform == osWindows && i.arch == archARM64 {
		AddToPathIfMissing(filepath.Join(gobin, "windows_amd64"), i.log)
	}
}

// ensureMsys2InPath adds the MSYS2 ucrt64 bin directory to PATH when it exists
// (the home of mingw-w64 gcc on Windows).
func (i *Installer) ensureMsys2InPath() {
	msys2Bin := `C:\msys64\ucrt64\bin`
	if info, err := os.Stat(msys2Bin); err == nil && info.IsDir() {
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
		if gobin := resolveGoBin(); gobin != "" {
			AddToPathIfMissing(gobin, i.log)
			if i.platform == osWindows && i.arch == archARM64 {
				AddToPathIfMissing(filepath.Join(gobin, "windows_amd64"), i.log)
			}
		}
	case method.Command == "powershell" && i.platform == osWindows:
		if tool.Binary == "gcc" {
			i.ensureMsys2InPath()
		} else if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			AddToPathIfMissing(
				filepath.Join(localAppData, "Programs", tool.Binary),
				i.log,
			)
		}
	}
}

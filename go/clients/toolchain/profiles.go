package toolchain

// GolangciLintArgs returns the golangci-lint run arguments.
func GolangciLintArgs(fix bool) []string {
	args := []string{cmdRun, "--allow-parallel-runners", "./..."}
	if fix {
		args = append(args, "--fix")
	}
	return args
}

// GofumptArgs returns gofumpt arguments for a single path.
func GofumptArgs(path string) []string {
	return []string{"-l", "-w", path}
}

// RuffCheckArgs returns ruff check arguments.
func RuffCheckArgs(fix, fast bool) []string {
	args := []string{cmdRun, "ruff", "check", "."}
	if fix {
		args = append(args, "--fix")
	}
	if fast {
		args = append(args, "--select", "E")
	}
	return args
}

// RuffFormatArgs returns ruff format arguments.
func RuffFormatArgs(check bool) []string {
	args := []string{cmdRun, "ruff", "format", "."}
	if check {
		args = append(args, "--check")
	}
	return args
}

// BasedpyrightArgs returns basedpyright arguments for a directory.
func BasedpyrightArgs(dir string) []string {
	return []string{cmdRun, "basedpyright", dir}
}

// GoVetArgs returns go vet arguments.
func GoVetArgs() []string {
	return []string{"vet", "./..."}
}

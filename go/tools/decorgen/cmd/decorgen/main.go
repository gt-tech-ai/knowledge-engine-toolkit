// Command decorgen writes the decorators decorgen.Generate builds, for a go:generate
// directive:
//
//	//go:generate go run github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/decorgen/cmd/decorgen -source ../core/interfaces -out decorated_gen.go DocumentStore
//
// The generated file's package is the one the directive sits in (GOPACKAGE), and its
// import path is resolved from the output directory, so the package's own types are
// written unqualified.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/tools/go/packages"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/decorgen"
)

// main parses the flags, generates, and writes the file; a failure exits 1.
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run generates the decorators args select.
func run(args []string) error {
	fs := flag.NewFlagSet("decorgen", flag.ContinueOnError)
	source := fs.String("source", ".", "package holding the interfaces")
	out := fs.String("out", "decorated_gen.go", "output file")
	pkg := fs.String("package", os.Getenv("GOPACKAGE"), "output package name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := filepath.Dir(*out)
	src, err := decorgen.Generate(decorgen.Config{
		Source:      *source,
		Package:     *pkg,
		PackagePath: importPath(dir),
		Interfaces:  fs.Args(),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(*out, src, 0o600)
}

// importPath resolves the import path of the package in dir, or "" when it has none.
func importPath(dir string) string {
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedName, Dir: dir}, ".")
	if err != nil || len(pkgs) != 1 {
		return ""
	}
	return pkgs[0].PkgPath
}

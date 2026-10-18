// Command opmockgen writes the operation-contract mocks opmockgen.Generate builds, for a
// go:generate directive in a mocks package:
//
//	//go:generate go run github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/opmockgen/cmd/opmockgen -source ../../core/interfaces -out mock_ops_gen.go OrganizationRepository
//
// The generated file's package name and import path are resolved from the output
// directory (-package overrides the name).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/tools/go/packages"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/opmockgen"
)

// main parses the flags, generates, and writes the file; a failure exits 1.
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run generates the mocks args select.
func run(args []string) error {
	fs := flag.NewFlagSet("opmockgen", flag.ContinueOnError)
	source := fs.String("source", ".", "package holding the contracts")
	out := fs.String("out", "mock_ops_gen.go", "output file")
	pkg := fs.String("package", "", "output package name (default: the output directory's)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name, path := outputPackage(filepath.Dir(*out))
	if *pkg != "" {
		name = *pkg
	}
	src, err := opmockgen.Generate(opmockgen.Config{
		Source:      *source,
		Package:     name,
		PackagePath: path,
		Structs:     fs.Args(),
	})
	if err != nil {
		return err
	}
	return os.WriteFile(*out, src, 0o600)
}

// outputPackage resolves the name and import path of the package in dir, or "" for each
// when it has none.
func outputPackage(dir string) (name, path string) {
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedName, Dir: dir}, ".")
	if err != nil || len(pkgs) != 1 {
		return "", ""
	}
	return pkgs[0].Name, pkgs[0].PkgPath
}

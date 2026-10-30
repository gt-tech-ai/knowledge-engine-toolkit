// Command errcodes renders the error-code taxonomy into its Go and Python outputs:
// `go generate ./go/core/errors/...` runs it from that package with -root at the
// repository root.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	fscodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase/fs"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/gen"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/errcodes"
)

// main parses -root and writes the outputs, exiting 1 on any error.
func main() {
	root := flag.String("root", ".", "the repository root")
	flag.Parse()
	if err := run(context.Background(), *root); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "errcodes:", err)
		os.Exit(1)
	}
}

// run renders the taxonomy under root and writes each output beneath it.
func run(ctx context.Context, root string) error {
	cb := codebase.New(fscodebase.New(root), codebase.NewSnapshot("errcodes"), nil)
	out, err := errcodes.New().Render(ctx, cb)
	if err != nil {
		return err
	}
	return gen.Apply(ctx, func(p string, data []byte) error {
		dst := filepath.Join(root, filepath.FromSlash(p))
		// A committed source file, readable like every other one.
		err := os.WriteFile(dst, data, 0o644) //nolint:gosec // G306: a source file
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "write "+p)
		}
		return nil
	}, out)
}

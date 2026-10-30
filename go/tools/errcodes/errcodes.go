// Package errcodes renders the error-code taxonomy (taxonomy/error-codes.yaml) into the
// Go ErrorCode constants and lookups and the Python ErrorCode enum and maps.
package errcodes

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// The taxonomy source and the two outputs, repository-relative.
const (
	// SourcePath is the taxonomy every output derives from.
	SourcePath = "taxonomy/error-codes.yaml"

	// GoPath is the rendered Go file.
	GoPath = "go/core/errors/codes.go"

	// PythonPath is the rendered Python module.
	PythonPath = "python/techai_webutils/src/techai_webutils/core/errors/codes.py"
)

// generator renders the taxonomy.
type generator struct{}

// Compile-time assertion that generator satisfies Generator.
var _ interfaces.Generator = generator{}

// New returns the error-code generator.
func New() interfaces.Generator { return generator{} }

// Name returns the generator's name.
func (generator) Name() string { return "errcodes" }

// Inputs selects the taxonomy source.
func (generator) Inputs() types.FileSelector {
	return types.FileSelector{Paths: []string{SourcePath}}
}

// Package resmetrics renders the resilience metric taxonomy
// (taxonomy/resilience-metrics.yaml) into the Go and Python series recorders, so both
// languages export the same series names, labels and label values.
package resmetrics

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// The taxonomy source and the two outputs, repository-relative.
const (
	// SourcePath is the taxonomy every output derives from.
	SourcePath = "taxonomy/resilience-metrics.yaml"

	// GoPath is the rendered Go file.
	GoPath = "go/foundation/resilience/series/series.go"

	// PythonPath is the rendered Python module.
	PythonPath = "python/techai_webutils/src/techai_webutils/foundation/resilience/series.py"
)

// maxEnumValues bounds an enum's values, so no label can grow a series without bound.
const maxEnumValues = 8

// generator renders the taxonomy.
type generator struct{}

// Compile-time assertion that generator satisfies Generator.
var _ interfaces.Generator = generator{}

// New returns the resilience metric generator.
func New() interfaces.Generator { return generator{} }

// Name returns the generator's name.
func (generator) Name() string { return "resmetrics" }

// Inputs selects the taxonomy source.
func (generator) Inputs() types.FileSelector {
	return types.FileSelector{
		Paths:            []string{SourcePath},
		Roots:            nil,
		Globs:            nil,
		Exts:             nil,
		Exclude:          nil,
		TrackedOnly:      false,
		IncludeGenerated: false,
		IncludeIgnored:   false,
	}
}

package impact

import (
	"regexp"
	"slices"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Class is a named set of path patterns: a change touches the class when a changed path
// matches any of them (a language by its file types, a domain by its directories).
type Class struct {
	// Name names the class (e.g. "go", "docs").
	Name string `yaml:"name"`

	// Patterns are the regular expressions a changed path is matched against.
	Patterns []string `yaml:"patterns"`
}

// Classify reports, for each class in order, whether a path in changed matches one of
// its patterns. A pattern that does not compile is CodeInvalidInput naming its class.
func Classify(classes []Class, changed []string) ([]bool, error) {
	out := make([]bool, len(classes))
	for i, c := range classes {
		for _, p := range c.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				return nil, apperr.Wrap(err, apperr.CodeInvalidInput,
					"change class "+c.Name+": pattern "+p)
			}
			if slices.ContainsFunc(changed, re.MatchString) {
				out[i] = true
				break
			}
		}
	}
	return out, nil
}

// SplitPaths splits `git diff --name-only -z` output (NUL-separated, newline-tolerant)
// into paths.
func SplitPaths(out string) []string {
	return strings.FieldsFunc(out, func(r rune) bool { return r == 0 || r == '\n' })
}

package impact

import (
	"regexp"
	"slices"
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	for index, c := range classes {
		hit, err := classMatches(c, changed)
		if err != nil {
			return nil, err
		}

		out[index] = hit
	}

	return out, nil
}

// classMatches reports whether any changed path matches one of the class's patterns,
// compiling them only until the first hit.
func classMatches(c Class, changed []string) (bool, error) {
	for _, p := range c.Patterns {
		pattern, err := regexp.Compile(p)
		if err != nil {
			return false, apperr.Wrap(err, apperr.CodeInvalidInput,
				"change class "+c.Name+": pattern "+p)
		}

		if slices.ContainsFunc(changed, pattern.MatchString) {
			return true, nil
		}
	}

	return false, nil
}

// SplitPaths splits `git diff --name-only -z` output (NUL-separated, newline-tolerant)
// into paths.
func SplitPaths(out string) []string {
	return strings.FieldsFunc(out, func(r rune) bool { return r == 0 || r == '\n' })
}

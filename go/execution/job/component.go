package job

import (
	"strings"
	"unicode"
)

// Component is the config component name of the job name in group under prefix:
// prefix, then the snake_case group and name, joined with dots ("cli", "Setup",
// "git hooks" → "cli.setup.git_hooks").
func Component(prefix, group, name string) string {
	return prefix + "." + snake(group) + "." + snake(name)
}

// snake lowercases s and joins its runs of letters and digits with underscores.
func snake(s string) string {
	var builder strings.Builder

	sep := false

	for _, count := range strings.ToLower(s) {
		if !unicode.IsLetter(count) && !unicode.IsDigit(count) {
			sep = true

			continue
		}

		if sep && builder.Len() > 0 {
			_ = builder.WriteByte('_')
		}

		_, _ = builder.WriteRune(count)

		sep = false
	}

	return builder.String()
}

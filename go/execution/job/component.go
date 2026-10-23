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
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if sep && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
			sep = false
			continue
		}
		sep = true
	}
	return b.String()
}

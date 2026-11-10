package yamlq

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// replaceScalar splices replacement's text over old's when both are scalars, parent is a
// block collection and only a comment follows old's token on its line, returning false
// when the splice does not apply. A scalar that wraps onto the next line is caught by
// the caller's holds check.
func replaceScalar(lines []string, parent, old, replacement *yaml.Node) ([]byte, bool) {
	if !spliceableScalar(lines, parent, old, replacement) {
		return nil, false
	}

	line := lines[old.Line-1]
	start := old.Column - 1

	end, found := scalarTokenEnd(line, start)
	if !found {
		return nil, false
	}

	token, found := singleLineToken(replacement)
	if !found {
		return nil, false
	}

	lines[old.Line-1] = line[:start] + token + line[end:]

	return joinLines(lines), true
}

// spliceableScalar reports whether old is a single-line scalar in a block collection,
// so its token can be replaced where it stands.
func spliceableScalar(lines []string, parent, old, replacement *yaml.Node) bool {
	if old.Kind != yaml.ScalarNode || replacement.Kind != yaml.ScalarNode {
		return false
	}

	if parent.Style&yaml.FlowStyle != 0 ||
		old.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return false
	}

	return old.Line >= 1 && old.Line <= len(lines)
}

// scalarTokenEnd returns where the token at start ends, false when anything but a
// comment follows it on the line.
func scalarTokenEnd(line string, start int) (int, bool) {
	if start < 0 || start > len(line) {
		return 0, false
	}

	end := start + tokenLen(line[start:])
	rest := strings.TrimSpace(line[end:])

	return end, rest == "" || strings.HasPrefix(rest, "#")
}

// singleLineToken renders a scalar, false when its text would wrap across lines.
func singleLineToken(replacement *yaml.Node) (string, bool) {
	text, err := yaml.Marshal(replacement)
	if err != nil {
		return "", false
	}

	token := strings.TrimSuffix(string(text), textNewline)

	return token, !strings.Contains(token, textNewline)
}

// holds reports whether out parses and its value at path in document index is exactly
// replacement's: a scalar that wrapped onto the next line leaves its continuation behind,
// which a splice of its first line alone cannot remove.
func holds(out []byte, index int, path string, replacement *yaml.Node) bool {
	docs, err := DecodeAll(out)
	if err != nil || index >= len(docs) {
		return false
	}

	got, err := Path(docs[index], path)

	return err == nil && len(got) == 1 && got[0].Node.Kind == yaml.ScalarNode &&
		got[0].Value == replacement.Value
}

// tokenLen is the length of the scalar token that starts s: a quoted string through its
// closing quote, else everything up to a ` #` comment, trailing spaces trimmed.
func tokenLen(text string) int {
	if text != "" && (text[0] == '"' || text[0] == '\'') {
		return quotedTokenLen(text)
	}

	end := len(text)
	if i := strings.Index(text, " #"); i >= 0 {
		end = i
	}

	return len(strings.TrimRight(text[:end], " \t"))
}

// quotedTokenLen is the length of the quoted string that starts s, through its closing
// quote; an unterminated string runs to the end.
func quotedTokenLen(text string) int {
	count := text[0]

	for index := 1; index < len(text); index++ {
		switch {
		case count == '"' && text[index] == '\\':
			index++
		case text[index] == count &&
			count == '\'' && index+1 < len(text) && text[index+1] == '\'':
			index++
		case text[index] == count:
			return index + 1
		default:
			// No other value changes the outcome.
		}
	}

	return len(text)
}

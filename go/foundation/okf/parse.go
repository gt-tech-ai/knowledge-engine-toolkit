package okf

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// fence is the line that opens and closes a frontmatter block.
const fence = "---"

// keyOKFVersion is the bundle-root index.md key declaring the format version.
const keyOKFVersion = "okf_version"

// bom is the UTF-8 byte-order mark some editors prepend to a file.
const bom = "\ufeff"

// Parse splits a markdown file into its frontmatter, body and links. A file whose first
// line is not `---` has no frontmatter and is all body. Unterminated, invalid or
// non-mapping frontmatter is CodeInvalidInput naming path.
func Parse(path string, data []byte) (Doc, error) {
	src := string(data)
	doc := Doc{Path: path, Body: src, BodyStartLine: 1}
	text := strings.TrimPrefix(src, bom)
	first, rest, _ := strings.Cut(text, "\n")

	if strings.TrimSuffix(first, "\r") == fence {
		block, body, line, ok := splitFrontmatter(rest)
		if !ok {
			return Doc{}, invalid(path, "the frontmatter block has no closing `---` line")
		}

		fm, err := decodeFrontmatter(path, block)
		if err != nil {
			return Doc{}, err
		}

		doc.HasFrontmatter, doc.Frontmatter = true, fm
		doc.Body, doc.BodyStartLine = body, line
	}

	doc.Links = extractLinks(doc.Body, doc.BodyStartLine)

	return doc, nil
}

// splitFrontmatter finds the closing fence in rest (the text after the opening fence)
// and returns the YAML between, the body after, and the body's 1-based file line.
func splitFrontmatter(rest string) (block, body string, bodyLine int, ok bool) {
	offset := 0
	for n := 2; offset <= len(rest); n++ {
		line, _, more := strings.Cut(rest[offset:], "\n")
		if strings.TrimSuffix(line, "\r") == fence {
			end := min(offset+len(line)+1, len(rest))

			return rest[:offset], rest[end:], n + 1, true
		}

		if !more {
			break
		}

		offset += len(line) + 1
	}

	return "", "", 0, false
}

// invalid is the coded error for a malformed file.
func invalid(path, why string) error {
	return apperr.New(apperr.CodeInvalidInput, path+": "+why)
}

// decodeFrontmatter decodes a YAML block into a Frontmatter, keeping unknown keys.
func decodeFrontmatter(path, block string) (Frontmatter, error) {
	var root yaml.Node

	if err := yaml.Unmarshal([]byte(block), &root); err != nil {
		return Frontmatter{}, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			path+": invalid frontmatter YAML",
		)
	}

	if len(root.Content) == 0 {
		return Frontmatter{}, nil
	}

	node := root.Content[0]
	if node.Kind != yaml.MappingNode {
		return Frontmatter{}, invalid(path, "the frontmatter is not a YAML mapping")
	}

	var fm Frontmatter

	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		if err := decodeKey(&fm, key, value); err != nil {
			return Frontmatter{}, apperr.Wrap(
				err,
				apperr.CodeInvalidInput,
				path+": frontmatter key "+key,
			)
		}
	}

	return fm, nil
}

// stringKeys maps the scalar OKF keys to their Frontmatter fields.
func stringKeys(fm *Frontmatter) map[string]*string {
	return map[string]*string{
		"type": &fm.Type, "title": &fm.Title, "description": &fm.Description,
		"resource": &fm.Resource, "status": &fm.Status, "stale_after": &fm.StaleAfter,
		keyOKFVersion: &fm.OKFVersion,
	}
}

// decode decodes value into out as a coded error.
func decode(value *yaml.Node, out any) error {
	if err := value.Decode(out); err != nil {
		return apperr.Wrap(err, apperr.CodeInvalidInput, "decode the value")
	}

	return nil
}

// decodeKey decodes one frontmatter key into its field, or into Extra.
func decodeKey(fm *Frontmatter, key string, value *yaml.Node) error {
	if field, ok := stringKeys(fm)[key]; ok {
		return decode(value, field)
	}

	switch key {
	case "tags":
		return decode(value, &fm.Tags)
	case "generated":
		fm.Generated = &Attribution{}

		return decode(value, fm.Generated)
	case "verified":
		if value.Kind == yaml.MappingNode {
			fm.Verified = make([]Attribution, 1)

			return decode(value, &fm.Verified[0])
		}

		return decode(value, &fm.Verified)
	}

	var v any

	if err := decode(value, &v); err != nil {
		return err
	}

	if fm.Extra == nil {
		fm.Extra = map[string]any{}
	}

	fm.Extra[key] = v

	return nil
}

var (
	// inlineLink matches an inline link or image: [text](target "title").
	inlineLink = regexp.MustCompile(
		`!?\[[^\]]*\]\(\s*(<[^>]*>|[^)\s]+)(?:\s+(?:"[^"]*"|'[^']*'))?\s*\)`,
	)

	// refDefinition matches a reference definition line: [label]: target.
	refDefinition = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:\s*(<[^>]*>|\S+)`)

	// scheme matches a URL scheme such as `https:` or `mailto:`.
	scheme = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

// extractLinks lists the links in body, skipping fenced blocks and code spans; firstLine
// is the file line body starts on.
func extractLinks(body string, firstLine int) []Link {
	var (
		links []Link
		open  string
	)

	for i, line := range strings.Split(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if marker := fenceMarker(line); marker != "" {
			switch {
			case open == "":
				open = marker
			case strings.HasPrefix(marker, open[:1]):
				open = ""
			}

			continue
		}

		if open != "" {
			continue
		}

		line = blankCodeSpans(line)
		for _, m := range refDefinition.FindAllStringSubmatch(line, -1) {
			links = appendLink(links, m[1], firstLine+i)
		}

		for _, m := range inlineLink.FindAllStringSubmatch(line, -1) {
			links = appendLink(links, m[1], firstLine+i)
		}
	}

	return links
}

// fenceMarker returns the backtick or tilde run that opens or closes a fenced code
// block on line, or "" when line is not a fence.
func fenceMarker(line string) string {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 {
		return ""
	}

	for _, c := range []string{"`", "~"} {
		if strings.HasPrefix(trimmed, c+c+c) {
			return trimmed[:len(trimmed)-len(strings.TrimLeft(trimmed, c))]
		}
	}

	return ""
}

// blankCodeSpans replaces each inline code span (a backtick run up to a matching run)
// with spaces, so links inside code are not read.
func blankCodeSpans(line string) string {
	b := []byte(line)
	for i := 0; i < len(b); {
		if b[i] != '`' {
			i++

			continue
		}

		n := runLength(b, i)
		end := closingRun(b, i+n, n)

		if end < 0 {
			i += n

			continue
		}

		for j := i; j < end+n; j++ {
			b[j] = ' '
		}

		i = end + n
	}

	return string(b)
}

// runLength counts the backticks starting at i.
func runLength(b []byte, i int) int {
	n := 0
	for i+n < len(b) && b[i+n] == '`' {
		n++
	}

	return n
}

// closingRun finds the next run of exactly n backticks from i, or -1.
func closingRun(b []byte, i, n int) int {
	for i < len(b) {
		if b[i] != '`' {
			i++

			continue
		}

		run := runLength(b, i)
		if run == n {
			return i
		}

		i += run
	}

	return -1
}

// appendLink classifies raw and appends it, dropping an empty target.
func appendLink(links []Link, raw string, line int) []Link {
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">")

	target, fragment, _ := strings.Cut(raw, "#")

	if target == "" && fragment == "" {
		return links
	}

	link := Link{Target: target, Fragment: fragment, Line: line}
	switch {
	case target == "":
		link.Kind = LinkAnchor
	case strings.HasPrefix(target, "//") || scheme.MatchString(target):
		link.Kind = LinkExternal
	case strings.HasPrefix(target, "/"):
		link.Kind = LinkAbsolute
	default:
		link.Kind = LinkRelative
	}

	return append(links, link)
}

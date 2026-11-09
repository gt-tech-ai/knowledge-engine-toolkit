package okf

import (
	"bytes"
	"cmp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// yamlIndent is the indent of rendered YAML.
const yamlIndent = 2

// RenderFrontmatter writes fm as a `---`-fenced YAML block in a stable order: the OKF
// keys (okf_version, type, title, description, resource, tags, status, stale_after,
// generated, verified), then Extra's keys sorted. Empty fields are omitted; tags and
// attributions are flow style; a single verifier is a bare mapping.
func RenderFrontmatter(fm Frontmatter) []byte {
	node := &yaml.Node{Kind: yaml.MappingNode}

	addScalars(node, [][2]string{
		{keyOKFVersion, fm.OKFVersion},
		{"type", fm.Type},
		{"title", fm.Title},
		{"description", fm.Description},
		{"resource", fm.Resource},
	})
	addTags(node, fm.Tags)
	addScalars(node, [][2]string{{"status", fm.Status}, {"stale_after", fm.StaleAfter}})

	if fm.Generated != nil {
		addEntry(node, "generated", attribution(*fm.Generated))
	}

	addVerified(node, fm.Verified)
	addExtra(node, fm.Extra)

	var buf bytes.Buffer

	_, _ = buf.WriteString(fence + textNewline)

	if len(node.Content) > 0 {
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(yamlIndent)

		_ = enc.Encode(node) // a tree of strings and decoded values always encodes
		_ = enc.Close()
	}

	_, _ = buf.WriteString(fence + textNewline)

	return buf.Bytes()
}

// addEntry appends one key/value pair.
func addEntry(node *yaml.Node, key string, value *yaml.Node) {
	node.Content = append(node.Content, scalar(key), value)
}

// addScalars appends each non-empty {key, value} pair.
func addScalars(node *yaml.Node, pairs [][pairLen]string) {
	for _, kv := range pairs {
		if kv[1] != "" {
			addEntry(node, kv[0], scalar(kv[1]))
		}
	}
}

// addTags appends the tags as a flow sequence, when there are any.
func addTags(node *yaml.Node, tags []string) {
	if len(tags) == 0 {
		return
	}

	list := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, t := range tags {
		list.Content = append(list.Content, scalar(t))
	}

	addEntry(node, "tags", list)
}

// addVerified appends one attribution as a mapping and several as a sequence.
func addVerified(node *yaml.Node, verified []Attribution) {
	switch len(verified) {
	case 0:
	case 1:
		addEntry(node, "verified", attribution(verified[0]))
	default:
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, a := range verified {
			list.Content = append(list.Content, attribution(a))
		}

		addEntry(node, "verified", list)
	}
}

// addExtra appends the unrecognized keys in sorted order, dropping any that won't encode.
func addExtra(node *yaml.Node, extra map[string]any) {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	for _, k := range keys {
		var value yaml.Node

		err := value.Encode(extra[k])
		if err == nil {
			addEntry(node, k, &value)
		}
	}
}

// scalar is a string node that the encoder quotes whenever the plain form would read
// back as another type.
func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

// attribution is a flow-style {by, at} mapping.
func attribution(a Attribution) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle}

	m.Content = append(m.Content, scalar("by"), scalar(a.By))

	if a.At != "" {
		m.Content = append(m.Content, scalar("at"), scalar(a.At))
	}

	return m
}

// Stale reports whether the concept is stale at now: StaleAfter is set and now is on or
// after it. StaleAfter is an RFC 3339 datetime (a bare YYYY-MM-DD date is accepted as
// midnight UTC); any other value is CodeInvalidInput.
func (f Frontmatter) Stale(now time.Time) (bool, error) {
	if f.StaleAfter == "" {
		return false, nil
	}

	at, err := time.Parse(time.RFC3339, f.StaleAfter)
	if err != nil {
		at, err = time.Parse(time.DateOnly, f.StaleAfter)
	}

	if err != nil {
		return false, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"stale_after is not an RFC 3339 datetime",
		)
	}

	return !now.Before(at), nil
}

// RenderIndex writes an index.md body (OKF §8): entries with no section first, then
// one `# Section` group per section in name order, each entry
// `* [Title](Link) - Description`. Entries sort by title (case-insensitive), then
// link, so the output does not depend on input order.
func RenderIndex(entries []IndexEntry) []byte {
	sorted := append([]IndexEntry(nil), entries...)
	slices.SortStableFunc(sorted, compareIndexEntries)

	var (
		blocks []string
		block  strings.Builder
	)

	section := ""

	for i, e := range sorted {
		if i == 0 || e.Section != section {
			blocks = flushIndexBlock(blocks, &block)
			section = e.Section

			writeSectionHeading(&block, section)
		}

		_, _ = block.WriteString(entryLine(e))
	}

	blocks = flushIndexBlock(blocks, &block)

	return []byte(strings.Join(blocks, textNewline))
}

// compareIndexEntries orders by section, then title ignoring case, then link.
func compareIndexEntries(a, b IndexEntry) int {
	return cmp.Or(
		cmp.Compare(a.Section, b.Section),
		cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)),
		cmp.Compare(a.Link, b.Link),
	)
}

// flushIndexBlock moves a non-empty block into blocks.
func flushIndexBlock(blocks []string, block *strings.Builder) []string {
	if block.Len() == 0 {
		return blocks
	}

	blocks = append(blocks, block.String())
	block.Reset()

	return blocks
}

// writeSectionHeading writes a named section's heading.
func writeSectionHeading(block *strings.Builder, section string) {
	if section != "" {
		_, _ = block.WriteString("# " + section + "\n\n")
	}
}

// entryLine renders one index entry, escaping brackets in the title and folding the
// description onto one line.
func entryLine(e IndexEntry) string {
	title := strings.NewReplacer("[", `\[`, "]", `\]`).Replace(e.Title)
	line := "* [" + title + "](" + e.Link + ")"

	if d := strings.Join(strings.Fields(e.Description), " "); d != "" {
		line += " - " + d
	}

	return line + textNewline
}

// RenderRootIndex writes the bundle-root index.md: an okf_version-only frontmatter
// block, a blank line, then the RenderIndex body.
func RenderRootIndex(okfVersion string, entries []IndexEntry) []byte {
	head := RenderFrontmatter(Frontmatter{OKFVersion: okfVersion})

	return append(append(head, '\n'), RenderIndex(entries)...)
}

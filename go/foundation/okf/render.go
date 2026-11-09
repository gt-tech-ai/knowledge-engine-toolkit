package okf

import (
	"bytes"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// RenderFrontmatter writes fm as a `---`-fenced YAML block in a stable order: the OKF
// keys (okf_version, type, title, description, resource, tags, status, stale_after,
// generated, verified), then Extra's keys sorted. Empty fields are omitted; tags and
// attributions are flow style; a single verifier is a bare mapping.
func RenderFrontmatter(fm Frontmatter) []byte {
	node := &yaml.Node{Kind: yaml.MappingNode}
	add := func(key string, value *yaml.Node) {
		node.Content = append(node.Content, scalar(key), value)
	}

	for _, kv := range []struct{ key, value string }{
		{keyOKFVersion, fm.OKFVersion},
		{"type", fm.Type},
		{"title", fm.Title},
		{"description", fm.Description},
		{"resource", fm.Resource},
	} {
		if kv.value != "" {
			add(kv.key, scalar(kv.value))
		}
	}

	if len(fm.Tags) > 0 {
		tags := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, t := range fm.Tags {
			tags.Content = append(tags.Content, scalar(t))
		}

		add("tags", tags)
	}

	for _, kv := range []struct{ key, value string }{
		{"status", fm.Status}, {"stale_after", fm.StaleAfter},
	} {
		if kv.value != "" {
			add(kv.key, scalar(kv.value))
		}
	}

	if fm.Generated != nil {
		add("generated", attribution(*fm.Generated))
	}

	switch len(fm.Verified) {
	case 0:
	case 1:
		add("verified", attribution(fm.Verified[0]))
	default:
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, a := range fm.Verified {
			list.Content = append(list.Content, attribution(a))
		}

		add("verified", list)
	}

	keys := make([]string, 0, len(fm.Extra))
	for k := range fm.Extra {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		var value yaml.Node

		if err := value.Encode(fm.Extra[k]); err == nil {
			add(k, &value)
		}
	}

	var buf bytes.Buffer

	buf.WriteString(fence + "\n")

	if len(node.Content) > 0 {
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)

		_ = enc.Encode(node) // a tree of strings and decoded values always encodes
		_ = enc.Close()
	}

	buf.WriteString(fence + "\n")

	return buf.Bytes()
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
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Section != b.Section {
			return a.Section < b.Section
		}

		if ta, tb := strings.ToLower(a.Title), strings.ToLower(b.Title); ta != tb {
			return ta < tb
		}

		return a.Link < b.Link
	})

	var (
		blocks []string
		block  strings.Builder
	)

	section := ""

	for i, e := range sorted {
		if i == 0 || e.Section != section {
			if block.Len() > 0 {
				blocks = append(blocks, block.String())
				block.Reset()
			}

			section = e.Section
			if section != "" {
				block.WriteString("# " + section + "\n\n")
			}
		}

		block.WriteString(entryLine(e))
	}

	if block.Len() > 0 {
		blocks = append(blocks, block.String())
	}

	return []byte(strings.Join(blocks, "\n"))
}

// entryLine renders one index entry, escaping brackets in the title and folding the
// description onto one line.
func entryLine(e IndexEntry) string {
	title := strings.NewReplacer("[", `\[`, "]", `\]`).Replace(e.Title)
	line := "* [" + title + "](" + e.Link + ")"

	if d := strings.Join(strings.Fields(e.Description), " "); d != "" {
		line += " - " + d
	}

	return line + "\n"
}

// RenderRootIndex writes the bundle-root index.md: an okf_version-only frontmatter
// block, a blank line, then the RenderIndex body.
func RenderRootIndex(okfVersion string, entries []IndexEntry) []byte {
	head := RenderFrontmatter(Frontmatter{OKFVersion: okfVersion})

	return append(append(head, '\n'), RenderIndex(entries)...)
}

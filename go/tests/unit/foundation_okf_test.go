package unit_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/okf"
)

// okfConcept is a concept with every named key, an extension key and a two-line body.
const okfConcept = `---
type: Guide
title: Add an endpoint
description: How to add one RPC end to end.
resource: apps/go/server/api
tags: [api, howto]
status: stable
stale_after: 2027-01-01T00:00:00Z
generated: {by: human:ada, at: 2026-10-01T09:00:00Z}
verified: {by: claude-code/claude-opus-4-8, at: 2026-10-31T10:00:00Z}
linear: ENG-1
---

# Add an endpoint

See [the contract](../contract.md#rules).
`

// TestOKFParse_FrontmatterAndBody tests that Parse splits a file into typed
// frontmatter, body and body start line.
//
// Why this test is important:
//   - Every OKF guard and generator reads concepts through Parse; a key decoded into
//     the wrong field, or a body offset by a line, makes every finding wrong.
//
// What it tests:
//   - Each named key lands in its field, a bare `verified` mapping becomes a
//     one-element list, an unknown key is kept in Extra, the body is the text after
//     the closing fence and BodyStartLine is that line.
func TestOKFParse_FrontmatterAndBody(t *testing.T) {
	t.Parallel()

	doc, err := okf.Parse("docs/guides/endpoint.md", []byte(okfConcept))
	require.NoError(t, err)

	fm := doc.Frontmatter
	assert.True(t, doc.HasFrontmatter)
	assert.Equal(t, textGuide, fm.Type)
	assert.Equal(t, "Add an endpoint", fm.Title)
	assert.Equal(t, "How to add one RPC end to end.", fm.Description)
	assert.Equal(t, "apps/go/server/api", fm.Resource)
	assert.Equal(t, []string{textAPI, "howto"}, fm.Tags)
	assert.Equal(t, "stable", fm.Status)
	assert.Equal(t, "2027-01-01T00:00:00Z", fm.StaleAfter)
	require.NotNil(t, fm.Generated)
	assert.Equal(
		t,
		okf.Attribution{By: textHumanAda, At: "2026-10-01T09:00:00Z"},
		*fm.Generated,
	)
	assert.Equal(
		t,
		[]okf.Attribution{
			{By: "claude-code/claude-opus-4-8", At: "2026-10-31T10:00:00Z"},
		},
		fm.Verified,
	)
	assert.Equal(t, map[string]any{textLinear: "ENG-1"}, fm.Extra)
	assert.Equal(
		t,
		"\n# Add an endpoint\n\nSee [the contract](../contract.md#rules).\n",
		doc.Body,
	)
	assert.Equal(t, wantBodyStartLine13, doc.BodyStartLine)
	assert.True(t, strings.HasSuffix(okfConcept, doc.Body))
}

// TestOKFParse_VerifiedList tests that a `verified` list keeps every entry.
//
// Why this test is important:
//   - OKF allows both a bare mapping and a list; dropping entries loses the human
//     sign-off that makes a concept human-reviewed.
//
// What it tests:
//   - A two-entry list parses to two attributions in order.
func TestOKFParse_VerifiedList(t *testing.T) {
	t.Parallel()

	src := "---\ntype: Guide\nverified:\n" +
		"  - {by: human:ada, at: 2026-10-01T00:00:00Z}\n  - {by: process:nightly}\n---\n"
	doc, err := okf.Parse(textAMd, []byte(src))
	require.NoError(t, err)
	assert.Equal(t, []okf.Attribution{
		{By: textHumanAda, At: "2026-10-01T00:00:00Z"},
		{By: "process:nightly"},
	}, doc.Frontmatter.Verified)
}

// TestOKFParse_NoFrontmatter tests a file without a frontmatter block.
//
// Why this test is important:
//   - Most files have no frontmatter until they are migrated; Parse must still return
//     their body and links so the link guards work before the migration.
//
// What it tests:
//   - HasFrontmatter is false, the whole file is the body starting on line 1, and a
//     `---` that is not the first line is body text, not a fence.
func TestOKFParse_NoFrontmatter(t *testing.T) {
	t.Parallel()

	src := "# Title\n\n---\ntype: Guide\n---\n"
	doc, err := okf.Parse(textAMd, []byte(src))
	require.NoError(t, err)
	assert.False(t, doc.HasFrontmatter)
	assert.Equal(t, okf.Frontmatter{}, doc.Frontmatter)
	assert.Equal(t, src, doc.Body)
	assert.Equal(t, 1, doc.BodyStartLine)
}

// TestOKFParse_InvalidFrontmatter tests that malformed frontmatter is a coded error.
//
// Why this test is important:
//   - A guard must name the broken file; an uncoded or path-less error sends the
//     reader hunting through 1,000 files.
//
// What it tests:
//   - Invalid YAML, an unterminated block and a non-mapping block each return
//     CodeInvalidInput whose message names the file.
func TestOKFParse_InvalidFrontmatter(t *testing.T) {
	t.Parallel()

	for name, src := range map[string]string{
		"invalid yaml":  "---\ntype: [unclosed\n---\nbody\n",
		"unterminated":  "---\ntype: Guide\nbody\n",
		"not a mapping": "---\n- a\n- b\n---\n",
	} {
		_, err := okf.Parse("docs/x.md", []byte(src))
		require.Error(t, err, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), name)
		assert.Contains(t, err.Error(), "docs/x.md", name)
	}
}

// TestOKFParse_CRLFAndBOM tests Windows line endings and a byte-order mark.
//
// Why this test is important:
//   - Files edited on Windows or exported from other tools carry CRLF or a BOM; a
//     parser that misses the fence reports them as having no frontmatter.
//
// What it tests:
//   - A BOM-prefixed CRLF file parses its type and its body.
func TestOKFParse_CRLFAndBOM(t *testing.T) {
	t.Parallel()

	src := "\ufeff---\r\ntype: Guide\r\n---\r\n# T\r\n"
	doc, err := okf.Parse(textAMd, []byte(src))
	require.NoError(t, err)
	assert.Equal(t, textGuide, doc.Frontmatter.Type)
	assert.Equal(t, "# T\r\n", doc.Body)
	assert.Equal(t, wantBodyStartLine4, doc.BodyStartLine)
}

// TestOKFParse_LinksClassified tests link extraction and classification.
//
// Why this test is important:
//   - The link guard rejects bundle-absolute links and checks relative ones; a link
//     misclassified, or one read out of a code sample, is a false finding.
//
// What it tests:
//   - Inline links, images and reference definitions are found with their file line,
//     fragment and kind; links inside a fenced block or an inline code span are not.
func TestOKFParse_LinksClassified(t *testing.T) {
	t.Parallel()

	src := strings.Join([]string{
		textDashDashDash,
		"type: Guide",
		textDashDashDash,
		"[rel](../a/b.md#sec) and ![img](./x.png)",
		"[abs](/docs/c.md) [anchor](#top) [ext](https://example.com/x) [mail](mailto:a@b.c)",
		"`[code](skip.md)` and [proto](//cdn.example.com/y)",
		"```",
		"[fenced](skip2.md)",
		"```",
		"[ref]: <../d.md>",
		"[t](e.md \"title\")",
	}, textNewline)
	doc, err := okf.Parse(textAMd, []byte(src))
	require.NoError(t, err)
	assert.Equal(t, []okf.Link{
		{Target: "../a/b.md", Fragment: "sec", Line: 4, Kind: okf.LinkRelative},
		{Target: "./x.png", Line: 4, Kind: okf.LinkRelative},
		{Target: "/docs/c.md", Line: 5, Kind: okf.LinkAbsolute},
		{Fragment: "top", Line: 5, Kind: okf.LinkAnchor},
		{Target: "https://example.com/x", Line: 5, Kind: okf.LinkExternal},
		{Target: "mailto:a@b.c", Line: 5, Kind: okf.LinkExternal},
		{Target: "//cdn.example.com/y", Line: 6, Kind: okf.LinkExternal},
		{Target: "../d.md", Line: 10, Kind: okf.LinkRelative},
		{Target: "e.md", Line: 11, Kind: okf.LinkRelative},
	}, doc.Links)
}

// TestOKFRenderFrontmatter_RoundTrip tests that rendered frontmatter parses back.
//
// Why this test is important:
//   - The frontmatter generator stamps 1,000+ files; a key it writes and Parse reads
//     differently corrupts every one, and an unstable key order churns every diff.
//
// What it tests:
//   - Rendering then parsing returns the same Frontmatter; keys come out in the OKF
//     order with extensions sorted last; a single verifier renders as a bare mapping;
//     empty fields are omitted.
func TestOKFRenderFrontmatter_RoundTrip(t *testing.T) {
	t.Parallel()

	fm := okf.Frontmatter{
		Type:        "Story",
		Title:       "Story 1.2 — Add: a thing",
		Description: "Adds the thing.",
		Tags:        []string{"epic-1"},
		Status:      "deprecated",
		Generated:   &okf.Attribution{By: textHumanAda, At: "2026-01-02T03:04:05Z"},
		Verified:    []okf.Attribution{{By: "human:bob", At: "2026-02-02T00:00:00Z"}},
		Extra:       map[string]any{"zeta": "z", textLinear: "ENG-9"},
	}
	out := okf.RenderFrontmatter(fm)
	assert.Equal(t, `---
type: Story
title: 'Story 1.2 — Add: a thing'
description: Adds the thing.
tags: [epic-1]
status: deprecated
generated: {by: 'human:ada', at: "2026-01-02T03:04:05Z"}
verified: {by: 'human:bob', at: "2026-02-02T00:00:00Z"}
linear: ENG-9
zeta: z
---
`, string(out))

	doc, err := okf.Parse(textAMd, out)
	require.NoError(t, err)
	assert.Equal(t, fm, doc.Frontmatter)
	assert.Empty(t, doc.Body)
}

// TestOKFFrontmatter_Stale tests the stale_after comparison.
//
// Why this test is important:
//   - The stale-doc guard flags reader docs past their date; a wrong comparison either
//     hides stale runbooks or flags fresh ones.
//
// What it tests:
//   - now >= stale_after is stale; earlier is not; no stale_after never is; an
//     unparseable value is CodeInvalidInput.
func TestOKFFrontmatter_Stale(t *testing.T) {
	t.Parallel()

	at := time.Date(year2026, month6, 1, 0, 0, 0, 0, time.UTC)
	fm := okf.Frontmatter{StaleAfter: "2026-06-01T00:00:00Z"}

	stale, err := fm.Stale(at)
	require.NoError(t, err)
	assert.True(t, stale)

	stale, err = fm.Stale(at.Add(-time.Second))
	require.NoError(t, err)
	assert.False(t, stale)

	stale, err = okf.Frontmatter{}.Stale(at)
	require.NoError(t, err)
	assert.False(t, stale)

	_, err = okf.Frontmatter{StaleAfter: "soon"}.Stale(at)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}

// TestOKFRenderIndex_GroupsAndSorts tests the index.md body.
//
// Why this test is important:
//   - index.md is generated and drift-checked; a format that differs from OKF §8 or
//     an order that depends on input order fails the drift check at random.
//
// What it tests:
//   - Unsectioned entries come first, then sections by name; entries sort by title
//     case-insensitively; an empty description omits ` - `; shuffled input renders
//     identical bytes.
func TestOKFRenderIndex_GroupsAndSorts(t *testing.T) {
	t.Parallel()

	entries := []okf.IndexEntry{
		{Title: "zeta", Link: "zeta.md", Description: "Last.", Section: textGuides},
		{Title: "Alpha", Link: "alpha.md", Description: "First.", Section: textGuides},
		{Title: "Runbooks", Link: "runbooks/", Section: "Areas"},
		{Title: "Overview", Link: "overview.md", Description: "Start here."},
	}
	want := `* [Overview](overview.md) - Start here.

# Areas

* [Runbooks](runbooks/)

# Guides

* [Alpha](alpha.md) - First.
* [zeta](zeta.md) - Last.
`
	assert.Equal(t, want, string(okf.RenderIndex(entries)))

	shuffled := []okf.IndexEntry{entries[index3], entries[1], entries[index2], entries[0]}
	assert.Equal(t, want, string(okf.RenderIndex(shuffled)))
}

// TestOKFRenderRootIndex_OKFVersion tests the bundle-root index.md.
//
// Why this test is important:
//   - The root index is the only index allowed frontmatter, and only okf_version.
//
// What it tests:
//   - The output opens with an okf_version-only block, then the RenderIndex body.
func TestOKFRenderRootIndex_OKFVersion(t *testing.T) {
	t.Parallel()

	got := okf.RenderRootIndex(
		"0.2",
		[]okf.IndexEntry{{Title: textGuides, Link: "guides/"}},
	)
	assert.Equal(
		t,
		"---\nokf_version: \"0.2\"\n---\n\n* [Guides](guides/)\n",
		string(got),
	)

	doc, err := okf.Parse(textDocsIndexMd, got)
	require.NoError(t, err)
	assert.Equal(t, "0.2", doc.Frontmatter.OKFVersion)
}

// okfRules is a two-type vocabulary used by the Conform tests.
func okfRules() okf.Rules {
	return okf.Rules{
		RequiredKeys: map[string][]string{
			textGuide: {textType, textTitle, textDescription},
			"Runbook": {textType, textTitle, textTags},
		},
		RootIndex: textDocsIndexMd,
	}
}

// conformRules parses src at path and returns the rule names Conform reports.
func conformRules(t *testing.T, path, src string, rules okf.Rules) []string {
	t.Helper()

	doc, err := okf.Parse(path, []byte(src))
	require.NoError(t, err)

	findings := okf.Conform(doc, rules)
	got := make([]string, 0, len(findings))

	for _, f := range findings {
		assert.Equal(t, path, f.Path)
		assert.NotEmpty(t, f.Message)

		got = append(got, f.Rule)
	}

	return got
}

// TestOKFConform_Concepts tests the concept rules.
//
// Why this test is important:
//   - These rules are the blocking `search check okf` gate; a missed rule lets a
//     non-conformant doc in, a false one blocks every PR.
//
// What it tests:
//   - A complete concept passes; no frontmatter, an empty type, an unknown type, and
//     each absent or empty required key are reported; with no vocabulary any type
//     passes.
func TestOKFConform_Concepts(t *testing.T) {
	t.Parallel()

	ok := "---\ntype: Guide\ntitle: T\ndescription: D\n---\n"
	assert.Empty(t, conformRules(t, textDocsGMd, ok, okfRules()))
	assert.Equal(
		t,
		[]string{okf.RuleNoFrontmatter},
		conformRules(t, textDocsGMd, "# T\n", okfRules()),
	)
	assert.Equal(
		t,
		[]string{okf.RuleMissingKey},
		conformRules(t, textDocsGMd, "---\ntitle: T\n---\n", okfRules()),
	)
	assert.Equal(
		t,
		[]string{okf.RuleUnknownType},
		conformRules(t, textDocsGMd, "---\ntype: Memo\n---\n", okfRules()),
	)
	assert.Equal(
		t,
		[]string{okf.RuleMissingKey, okf.RuleMissingKey},
		conformRules(
			t,
			"docs/r.md",
			"---\ntype: Runbook\ntitle: \"\"\ntags: []\n---\n",
			okfRules(),
		),
	)
	assert.Empty(t, conformRules(t, textDocsGMd, "---\ntype: Memo\n---\n", okf.Rules{}))
}

// TestOKFConform_ReservedNames tests index.md and log.md handling.
//
// Why this test is important:
//   - OKF reserves index.md and log.md; using one as a concept, or giving an index
//     frontmatter, breaks every consumer's directory listing.
//
// What it tests:
//   - A plain index.md and log.md pass without frontmatter; the root index may carry
//     okf_version only; any other index frontmatter is index-frontmatter; a typed
//     index.md or a log.md with frontmatter is reserved-name.
func TestOKFConform_ReservedNames(t *testing.T) {
	t.Parallel()
	assert.Empty(t, conformRules(t, textDocsAIndexMd, "* [x](x.md)\n", okfRules()))
	assert.Empty(t, conformRules(t, "docs/a/log.md", "## 2026-01-01\n", okfRules()))
	assert.Empty(
		t,
		conformRules(t, textDocsIndexMd, "---\nokf_version: \"0.2\"\n---\n", okfRules()),
	)
	assert.Equal(
		t,
		[]string{okf.RuleIndexFrontmatter},
		conformRules(
			t,
			textDocsAIndexMd,
			"---\nokf_version: \"0.2\"\n---\n",
			okfRules(),
		),
	)
	assert.Equal(
		t,
		[]string{okf.RuleIndexFrontmatter},
		conformRules(
			t,
			textDocsIndexMd,
			"---\nokf_version: \"0.2\"\ntitle: x\n---\n",
			okfRules(),
		),
	)
	assert.Equal(t, []string{okf.RuleReservedName},
		conformRules(t, textDocsAIndexMd, "---\ntype: Guide\n---\n", okfRules()))
	assert.Equal(t, []string{okf.RuleReservedName},
		conformRules(t, "docs/a/log.md", "---\ntype: Guide\n---\n", okfRules()))
}

// logRules returns the rule names ConformLog reports for src.
func logRules(src string) []string {
	findings := okf.ConformLog("docs/e/log.md", []byte(src))
	got := make([]string, 0, len(findings))

	for _, f := range findings {
		got = append(got, f.Rule)
	}

	return got
}

// TestOKFConformLog tests log.md headings.
//
// Why this test is important:
//   - Folded story history lives in log.md; an out-of-order or malformed date
//     heading misleads every reader of the epic's history.
//
// What it tests:
//   - Strictly descending YYYY-MM-DD `##` headings pass (a `#` title and a heading in
//     a fence are ignored); a non-date or impossible date is log-heading; an equal
//     or ascending date is log-order, reported on its line.
func TestOKFConformLog(t *testing.T) {
	t.Parallel()
	assert.Empty(
		t,
		logRules(
			"# Log\n\n## 2026-10-02\n* a\n\n```\n## nope\n```\n## 2026-10-01\n* b\n",
		),
	)
	assert.Equal(t, []string{okf.RuleLogHeading}, logRules("## Oct 1\n"))
	assert.Equal(t, []string{okf.RuleLogHeading}, logRules("## 2026-02-30\n"))
	assert.Equal(
		t,
		[]string{okf.RuleLogOrder},
		logRules("## 2026-10-01\n## 2026-10-02\n"),
	)
	assert.Equal(
		t,
		[]string{okf.RuleLogOrder},
		logRules("## 2026-10-01\n## 2026-10-01\n"),
	)

	f := okf.ConformLog("l.md", []byte("## 2026-10-01\n\n## 2026-10-03\n"))
	require.Len(t, f, 1)
	assert.Equal(t, wantLine3, f[0].Line)
}

// FuzzOKFParse tests that Parse never panics and keeps the body a suffix.
//
// Why this test is important:
//   - Parse reads every markdown file in the repository, including malformed ones;
//     a panic takes down the whole guard run.
//
// What it tests:
//   - For any input Parse returns without panicking, and on success the body is a
//     suffix of the input (BOM aside) and every link line is within the file.
func FuzzOKFParse(f *testing.F) {
	for _, seed := range []string{
		"", textDashDashDash, "---\n", "---\n---\n", "---\ntype: x\n", okfConcept,
		"\ufeff---\r\ntype: G\r\n---\r\n[a](b)\r\n", "```\n[a](b)\n",
		strings.Repeat(textX, count4096),
		"[a](<b c>) [d]: e\n`x` [f](g",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, src string) {
		fuzzOKFParseCase(t, src)
	})
}

// fuzzOKFParseCase is the body of a closure in FuzzOKFParse.
func fuzzOKFParseCase(t *testing.T, src string) {
	t.Helper()

	doc, err := okf.Parse("f.md", []byte(src))
	if err != nil {
		return
	}

	if !strings.HasSuffix(src, doc.Body) {
		t.Fatalf("body %q is not a suffix of %q", doc.Body, src)
	}

	lines := strings.Count(src, textNewline) + 1
	for _, l := range doc.Links {
		if l.Line < 1 || l.Line > lines {
			t.Fatalf("link line %d outside 1..%d", l.Line, lines)
		}
	}
}

// TestOKFHasKey tests key presence over named and extension keys.
//
// Why this test is important:
//   - Per-type required keys may name any OKF or extension key; a key read as present
//     when empty lets an incomplete concept pass the required-key guard.
//
// What it tests:
//   - Named scalar, list and attribution keys count only when non-empty; an extension
//     key counts when Extra holds a non-empty value.
func TestOKFHasKey(t *testing.T) {
	t.Parallel()

	fm := okf.Frontmatter{
		Title:     "T",
		Generated: &okf.Attribution{By: textHumanAda},
		Verified:  []okf.Attribution{{By: "human:bob"}},
		Extra:     map[string]any{textLinear: "ENG-1", textEmpty: "", textNull: nil},
	}
	for key, want := range map[string]bool{
		textTitle: true, textDescription: false, textTags: false, "generated": true,
		"verified": true, textLinear: true, textEmpty: false, textNull: false,
		textAbsent: false,
	} {
		assert.Equal(t, want, okf.HasKey(fm, key), key)
	}

	assert.False(
		t,
		okf.HasKey(okf.Frontmatter{Generated: &okf.Attribution{}}, "generated"),
	)
}

package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/paththeory"
)

// theoryTokens are the declared tokens the paththeory tests compile with.
func theoryTokens() map[string]paththeory.Token {
	return map[string]paththeory.Token{
		"tier": {Values: []string{"core", "stores", "services"}},
		"app":  {Pattern: `[a-z][a-z0-9-]*`},
	}
}

// TestPathTheory_ClaimsByFirstMatchingRule tests how a path is claimed.
//
// Why this test is important:
//   - Every file's placement and name is checked by the rule that claims it; a wrong
//     claim lets a misplaced or misnamed file pass, and a missed one fails a good file.
//
// What it tests:
//   - A template matches the whole path: tokens match within one folder, `**` spans any
//     number of folders (none included), closed-list tokens accept only their values.
//   - The first matching rule claims the path, so a specific rule above a general one
//     wins; a path no rule matches is unclaimed.
func TestPathTheory_ClaimsByFirstMatchingRule(t *testing.T) {
	t.Parallel()
	th, err := paththeory.Compile(theoryTokens(), []paththeory.Rule{
		{Kind: "unit test", Path: "apps/go/{app}/tests/unit/{tier}_{snake}_test.go"},
		{Kind: "go source", Path: "apps/go/{app}/**/{snake}.go"},
		{Kind: "adr", Path: "docs/decisions/{nnnn}-{slug}.md"},
		{Kind: "dated record", Path: "docs/delivery/audits/{date}-{slug}.md"},
	})
	require.NoError(t, err)

	cases := map[string]string{
		"apps/go/api/tests/unit/stores_document_test.go":  "unit test",
		"apps/go/api/tests/unit/connectorsync_x_test.go":  "go source",
		"apps/go/api/main.go":                             "go source",
		"apps/go/api/stores/a/b/doc_store.go":             "go source",
		"docs/decisions/0125-every-file.md":               "adr",
		"docs/delivery/audits/2026-11-02-naming-audit.md": "dated record",
	}
	for p, kind := range cases {
		r, ok := th.Claim(p)
		require.True(t, ok, p)
		assert.Equal(t, kind, r.Kind, p)
	}
	for _, p := range []string{
		"apps/go/Api/main.go",
		"apps/go/api/stores/DocStore.go",
		"docs/decisions/125-short.md",
		"docs/delivery/audits/naming-audit.md",
		"README.md",
	} {
		_, ok := th.Claim(p)
		assert.False(t, ok, p)
	}
}

// TestPathTheory_BuiltinTokens tests the name styles every theory can use.
//
// Why this test is important:
//   - The style tokens are how a rule says "this file is snake_case" or "PascalCase";
//     a token that matches too much makes the rule meaningless.
//
// What it tests:
//   - Each built-in accepts its style and rejects the others, and none crosses a folder.
func TestPathTheory_BuiltinTokens(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ good, bad []string }{
		"slug": {
			[]string{"cli-command", "a1"},
			[]string{"Cli", "cli_command", "-a", "a/b"},
		},
		"snake": {
			[]string{"doc_store", "a1"},
			[]string{"DocStore", "doc-store", "a/b"},
		},
		"pascal":  {[]string{"DocStore", "A1"}, []string{"docStore", "Doc_Store"}},
		"camel":   {[]string{"docStore", "use"}, []string{"DocStore", "doc-store"}},
		"segment": {[]string{"65.3-sweep", "0124-x"}, []string{"Bad_Dir", "a b"}},
		"date":    {[]string{"2026-11-02"}, []string{"2026-1-2"}},
		"nnnn":    {[]string{"0125"}, []string{"125"}},
		"nn":      {[]string{"07", "123"}, []string{"7"}},
		"n":       {[]string{"7"}, []string{"x"}},
		"any":     {[]string{"Any_Name.v2"}, []string{"a/b", ""}},
	}
	for tok, tc := range cases {
		th, err := paththeory.Compile(nil, []paththeory.Rule{
			{Kind: tok, Path: "x/{" + tok + "}.txt"},
		})
		require.NoError(t, err, tok)
		for _, g := range tc.good {
			_, ok := th.Claim("x/" + g + ".txt")
			assert.True(t, ok, "%s should accept %q", tok, g)
		}
		for _, b := range tc.bad {
			_, ok := th.Claim("x/" + b + ".txt")
			assert.False(t, ok, "%s should reject %q", tok, b)
		}
	}
}

// TestPathTheory_CompileRefuses tests the rule-set validation.
//
// Why this test is important:
//   - A catch-all rule claims every file and silently disables the theory; an unknown
//     token or a rule with no kind is a typo that would never match.
//
// What it tests:
//   - An unknown token, a bare `**` or `**/{any}` template, an empty kind or path, and a
//     declared token that is both a pattern and a list, or neither, are CodeInvalidInput.
func TestPathTheory_CompileRefuses(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		tokens map[string]paththeory.Token
		rule   paththeory.Rule
	}{
		"unknown token": {rule: paththeory.Rule{Kind: "k", Path: "a/{nope}.go"}},
		"catch-all":     {rule: paththeory.Rule{Kind: "k", Path: "**"}},
		"any catch-all": {rule: paththeory.Rule{Kind: "k", Path: "**/{any}"}},
		"no kind":       {rule: paththeory.Rule{Path: "a.go"}},
		"no path":       {rule: paththeory.Rule{Kind: "k"}},
		"both": {
			tokens: map[string]paththeory.Token{
				"t": {Pattern: "a", Values: []string{"a"}},
			},
			rule: paththeory.Rule{Kind: "k", Path: "{t}.go"},
		},
		"neither": {
			tokens: map[string]paththeory.Token{"t": {}},
			rule:   paththeory.Rule{Kind: "k", Path: "{t}.go"},
		},
	}
	for name, tc := range cases {
		_, err := paththeory.Compile(tc.tokens, []paththeory.Rule{tc.rule})
		require.Error(t, err, name)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}
}

// TestPathTheory_ClassifyReportsUnclaimedAndDeadRules tests a whole-tree classification.
//
// Why this test is important:
//   - The guard fails on an unclaimed file and on a rule that claims nothing (a dead or
//     shadowed rule); both must come out of one pass over the tree.
//
// What it tests:
//   - Classify returns each path's claiming rule, the paths no rule claims, and the
//     rules that claimed no path, in input order.
func TestPathTheory_ClassifyReportsUnclaimedAndDeadRules(t *testing.T) {
	t.Parallel()
	th, err := paththeory.Compile(nil, []paththeory.Rule{
		{Kind: "go", Path: "**/{snake}.go"},
		{Kind: "shadowed", Path: "pkg/{snake}.go"},
		{Kind: "md", Path: "docs/{slug}.md"},
	})
	require.NoError(t, err)

	c := th.Classify([]string{"pkg/a.go", "docs/b.md", "Makefile"})

	assert.Equal(t, map[string]string{"pkg/a.go": "go", "docs/b.md": "md"}, c.Kinds())
	assert.Equal(t, []string{"Makefile"}, c.Unclaimed)
	require.Len(t, c.Dead, 1)
	assert.Equal(t, "shadowed", c.Dead[0].Kind)
}

package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/paththeory"
)

// Literals the paththeory tests share.
const (
	// kindGoSource is the kind of the theory's general Go source rule.
	kindGoSource = "go source"

	// kindPlaceholder is the kind of a rule whose kind the test does not read.
	kindPlaceholder = "k"

	// crossFolder is a name that spans two folders, which no token may match.
	crossFolder = "a/b"

	// pascalName is a PascalCase name.
	pascalName = "DocStore"

	// builtinAny is the built-in token that matches any one segment.
	builtinAny = "any"
)

// pathRule is the plain rule of kind over path.
func pathRule(kind, path string) paththeory.Rule {
	return paththeory.Rule{Kind: kind, Path: path, Generator: "", Forbid: false}
}

// forbidRule is the forbid rule of kind over path.
func forbidRule(kind, path string) paththeory.Rule {
	return paththeory.Rule{Kind: kind, Path: path, Generator: "", Forbid: true}
}

// theoryTokens are the declared tokens the paththeory tests compile with.
func theoryTokens() map[string]paththeory.Token {
	return map[string]paththeory.Token{
		"tier": {Pattern: "", Values: []string{"core", "stores", "services"}},
		"app":  {Pattern: `[a-z][a-z0-9-]*`, Values: nil},
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

	theory, err := paththeory.Compile(theoryTokens(), []paththeory.Rule{
		pathRule("unit test", "apps/go/{app}/tests/unit/{tier}_{snake}_test.go"),
		pathRule(kindGoSource, "apps/go/{app}/**/{snake}.go"),
		pathRule("adr", "docs/decisions/{nnnn}-{slug}.md"),
		pathRule("dated record", "docs/delivery/audits/{date}-{slug}.md"),
	})
	require.NoError(t, err)

	cases := map[string]string{
		"apps/go/api/tests/unit/stores_document_test.go":  "unit test",
		"apps/go/api/tests/unit/connectorsync_x_test.go":  kindGoSource,
		"apps/go/api/main.go":                             kindGoSource,
		"apps/go/api/stores/a/b/doc_store.go":             kindGoSource,
		"docs/decisions/0125-every-file.md":               "adr",
		"docs/delivery/audits/2026-11-02-naming-audit.md": "dated record",
	}
	for path, kind := range cases {
		rule, claimed := theory.Claim(path)
		require.True(t, claimed, path)
		assert.Equal(t, kind, rule.Kind, path)
	}

	for _, path := range []string{
		"apps/go/Api/main.go",
		"apps/go/api/stores/DocStore.go",
		"docs/decisions/125-short.md",
		"docs/delivery/audits/naming-audit.md",
		"README.md",
	} {
		_, claimed := theory.Claim(path)
		assert.False(t, claimed, path)
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
			[]string{"Cli", "cli_command", "-a", crossFolder},
		},
		"snake": {
			[]string{"doc_store", "a1"},
			[]string{pascalName, "doc-store", crossFolder},
		},
		"pascal":   {[]string{pascalName, "A1"}, []string{"docStore", "Doc_Store"}},
		"camel":    {[]string{"docStore", "store"}, []string{pascalName, "doc-store"}},
		"segment":  {[]string{"65.3-sweep", "0124-x"}, []string{"Bad_Dir", "a b"}},
		"date":     {[]string{"2026-11-02"}, []string{"2026-1-2"}},
		"nnnn":     {[]string{"0125"}, []string{"125"}},
		"nn":       {[]string{"07", "123"}, []string{"7"}},
		"n":        {[]string{"7"}, []string{"x"}},
		builtinAny: {[]string{"Any_Name.v2"}, []string{crossFolder, ""}},
	}
	for token, examples := range cases {
		theory, err := paththeory.Compile(nil, []paththeory.Rule{
			pathRule(token, "x/{"+token+"}.txt"),
		})
		require.NoError(t, err, token)

		for _, good := range examples.good {
			_, claimed := theory.Claim("x/" + good + ".txt")
			assert.True(t, claimed, "%s should accept %q", token, good)
		}

		for _, bad := range examples.bad {
			_, claimed := theory.Claim("x/" + bad + ".txt")
			assert.False(t, claimed, "%s should reject %q", token, bad)
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
		"unknown token": {tokens: nil, rule: pathRule(kindPlaceholder, "a/{nope}.go")},
		"catch-all":     {tokens: nil, rule: pathRule(kindPlaceholder, "**")},
		"any catch-all": {tokens: nil, rule: pathRule(kindPlaceholder, "**/{any}")},
		"no kind":       {tokens: nil, rule: pathRule("", "a.go")},
		"no path":       {tokens: nil, rule: pathRule(kindPlaceholder, "")},
		"both": {
			tokens: map[string]paththeory.Token{
				"t": {Pattern: "a", Values: []string{"a"}},
			},
			rule: pathRule(kindPlaceholder, "{t}.go"),
		},
		"neither": {
			tokens: map[string]paththeory.Token{"t": {Pattern: "", Values: nil}},
			rule:   pathRule(kindPlaceholder, "{t}.go"),
		},
	}
	for name, testCase := range cases {
		_, err := paththeory.Compile(testCase.tokens, []paththeory.Rule{testCase.rule})
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

	theory, err := paththeory.Compile(nil, []paththeory.Rule{
		pathRule("go", "**/{snake}.go"),
		pathRule("shadowed", "pkg/{snake}.go"),
		pathRule("md", "docs/{slug}.md"),
	})
	require.NoError(t, err)

	result := theory.Classify([]string{"pkg/a.go", "docs/b.md", "Makefile"})

	assert.Equal(
		t,
		map[string]string{"pkg/a.go": "go", "docs/b.md": "md"},
		result.Kinds(),
	)
	assert.Equal(t, []string{"Makefile"}, result.Unclaimed)
	require.Len(t, result.Dead, 1)
	assert.Equal(t, "shadowed", result.Dead[0].Kind)
}

// TestPathTheory_ForbidRules tests a rule that names a violation.
//
// Why this test is important:
//   - A template cannot say "except": `stores/{snake}.go` also matches a test file, so a
//     forbid rule above it must claim that path as a named violation, or the looser
//     rule silently accepts a misplaced file.
//
// What it tests:
//   - A path whose first match is a forbid rule is Forbidden under that rule, neither
//     claimed nor unclaimed; Claim returns the forbid rule; a forbid rule that matches
//     nothing is not reported dead.
func TestPathTheory_ForbidRules(t *testing.T) {
	t.Parallel()

	const misplacedTest = "stores/doc_store_test.go"

	theory, err := paththeory.Compile(nil, []paththeory.Rule{
		forbidRule("test outside tests/", "stores/{snake}_test.go"),
		forbidRule("never", "never/{snake}.go"),
		pathRule("store", "stores/{snake}.go"),
	})
	require.NoError(t, err)

	result := theory.Classify([]string{"stores/doc_store.go", misplacedTest})

	assert.Equal(t, map[string]string{"stores/doc_store.go": "store"}, result.Kinds())
	assert.Empty(t, result.Unclaimed)
	assert.Empty(t, result.Dead)
	require.Contains(t, result.Forbidden, misplacedTest)
	assert.Equal(t, "test outside tests/", result.Forbidden[misplacedTest].Kind)

	rule, claimed := theory.Claim(misplacedTest)
	require.True(t, claimed)
	assert.True(t, rule.Forbid)
}

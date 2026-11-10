package unit_test

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientcodebase "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/codebase"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// Size budget for the trees under the ratchet: a function body, a file and a block
// nesting depth may not exceed these.
const (
	maxFuncLines = 50
	maxFileLines = 800
	maxNesting   = 4
)

// budgetRoots returns the Go trees held to the size budget, relative to the module
// root. A tree joins when a change first brings it to the budget; it never leaves.
func budgetRoots() []string {
	return []string{
		"workflows/outbox",
		"stores/outbox",
		"core/interfaces",
		"clients/messaging",
		"clients/connector",
	}
}

// sizeViolations returns one line per function, file or nesting depth over budget in
// the non-test Go files under roots.
func sizeViolations(t *testing.T, cb interfaces.Codebase, roots []string) []string {
	t.Helper()

	files, err := cb.Go().
		Parse(t.Context(), fixtures.BuildRef(func(row *types.FileSelector) {
			row.Roots = roots
			row.Exts = []string{textGoExt}
			row.Exclude = []string{"**/*_test.go"}
		}), types.ParseComments)
	require.NoError(t, err)

	var out []string

	for _, goFile := range files {
		if n := goFile.FileSet.File(goFile.AST.Pos()).LineCount(); n > maxFileLines {
			out = append(
				out,
				fmt.Sprintf("%s: %d lines (max %d)", goFile.Path, n, maxFileLines),
			)
		}

		out = append(out, fileFuncViolations(goFile)...)
	}

	return out
}

// fileFuncViolations checks every function with a body in the file.
func fileFuncViolations(f types.GoFile) []string {
	var out []string

	for _, decl := range f.AST.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			out = append(out, funcViolations(f.FileSet, f.Path, fn)...)
		}
	}

	return out
}

// funcViolations reports fn when its span or its block nesting exceeds the budget.
func funcViolations(fset *token.FileSet, path string, funcDecl *ast.FuncDecl) []string {
	var out []string

	span := fset.Position(funcDecl.End()).Line - fset.Position(funcDecl.Pos()).Line + 1

	if span > maxFuncLines {
		out = append(out, fmt.Sprintf("%s: func %s is %d lines (max %d)",
			path, funcDecl.Name.Name, span, maxFuncLines))
	}

	if depth := nesting(funcDecl.Body, 0); depth > maxNesting {
		out = append(out, fmt.Sprintf("%s: func %s nests %d deep (max %d)",
			path, funcDecl.Name.Name, depth, maxNesting))
	}

	return out
}

// nesting returns the deepest control-block nesting under n, counting from depth; a
// function literal starts its own count, as it is its own function.
func nesting(n ast.Node, depth int) int {
	deepest := depth

	ast.Inspect(n, func(child ast.Node) bool {
		if child == n {
			return true
		}

		switch node := child.(type) {
		case *ast.FuncLit:
			deepest = max(deepest, nesting(node.Body, 0))

			return false
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
			*ast.TypeSwitchStmt, *ast.SelectStmt:
			deepest = max(deepest, nesting(node, depth+1))

			return false
		}

		return true
	})

	return deepest
}

// TestCodeSizeBudget_TreesStayWithinBudget tests the ratchet over the budgeted trees.
//
// Why this test is important:
//   - Long functions and deep nesting are where these trees' defects hid; the budget
//     keeps a tree that was brought to standard from drifting back.
//
// What it tests:
//   - No non-test, non-generated Go file under budgetRoots has a function longer than
//     50 lines, a nesting depth over 4, or more than 800 lines.
func TestCodeSizeBudget_TreesStayWithinBudget(t *testing.T) {
	t.Parallel()

	codebase, err := clientcodebase.NewFromConfig(t.Context(), clientcodebase.Config{
		Kind: clientcodebase.KindFS,
		Root: filepath.Join(textDotDot, textDotDot),
		Name: "",
	}, new(clientcodebase.Deps))
	require.NoError(t, err)

	assert.Empty(t, sizeViolations(t, codebase, budgetRoots()))
}

// TestCodeSizeBudget_FlagsAnOverBudgetFunction tests that the ratchet catches violations.
//
// Why this test is important:
//   - A budget test that cannot fail guards nothing; this pins that it reads functions,
//     nesting and file length the way the budget states.
//
// What it tests:
//   - A 51-line function and a function nesting five blocks deep are both reported,
//     and a 50-line function with four levels is not.
func TestCodeSizeBudget_FlagsAnOverBudgetFunction(t *testing.T) {
	t.Parallel()

	long := "func Long() {\n" + strings.Repeat(
		"\t_ = 1\n",
		bodyLinesOverBudget,
	) + textBraceCloseNewline
	fits := "func Fits() {\n" + strings.Repeat(
		"\t_ = 1\n",
		bodyLinesAtBudget,
	) + textBraceCloseNewline
	deep := "func Deep() {\n" + strings.Repeat(
		"if true {\n",
		nestingOverBudget,
	) + strings.Repeat(
		textBraceCloseNewline,
		nestingOverBudget,
	) + textBraceCloseNewline
	four := "func Four() {\n" + strings.Repeat(
		"if true {\n",
		nestingAtBudget,
	) + strings.Repeat(
		textBraceCloseNewline,
		nestingAtBudget,
	) + textBraceCloseNewline
	codebase, err := clientcodebase.NewFromConfig(
		t.Context(),
		stdx.Zero[clientcodebase.Config](),
		fixtures.BuildRef(func(row *clientcodebase.Deps) {
			row.Files = map[string]string{
				"budget/a.go": "package budget\n\n" + long + fits + deep + four,
			}
		}),
	)
	require.NoError(t, err)

	got := sizeViolations(t, codebase, []string{"budget"})

	assert.Equal(t, []string{
		"budget/a.go: func Long is 51 lines (max 50)",
		"budget/a.go: func Deep nests 5 deep (max 4)",
	}, got)
}

const (
	// nestingAtBudget is how many blocks Four nests: the budget's maximum, so it passes.
	nestingAtBudget = 4

	// bodyLinesAtBudget is how many body lines Fits has; with its signature and closing
	// brace it is 50 lines, the budget's maximum.
	bodyLinesAtBudget = 48

	// bodyLinesOverBudget is how many body lines Long has: 51 lines in all, one over.
	bodyLinesOverBudget = 49

	// nestingOverBudget is how many blocks Deep nests: one over the budget's maximum.
	nestingOverBudget = 5
)

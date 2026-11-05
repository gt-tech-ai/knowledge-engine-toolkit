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
	files, err := cb.Go().Parse(t.Context(), types.FileSelector{
		Roots:   roots,
		Exts:    []string{".go"},
		Exclude: []string{"**/*_test.go"},
	}, types.ParseComments)
	require.NoError(t, err)
	var out []string
	for _, f := range files {
		if n := f.FileSet.File(f.AST.Pos()).LineCount(); n > maxFileLines {
			out = append(
				out,
				fmt.Sprintf("%s: %d lines (max %d)", f.Path, n, maxFileLines),
			)
		}
		for _, decl := range f.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			out = append(out, funcViolations(f.FileSet, f.Path, fn)...)
		}
	}
	return out
}

// funcViolations reports fn when its span or its block nesting exceeds the budget.
func funcViolations(fset *token.FileSet, path string, fn *ast.FuncDecl) []string {
	var out []string
	span := fset.Position(fn.End()).Line - fset.Position(fn.Pos()).Line + 1
	if span > maxFuncLines {
		out = append(out, fmt.Sprintf("%s: func %s is %d lines (max %d)",
			path, fn.Name.Name, span, maxFuncLines))
	}
	if depth := nesting(fn.Body, 0); depth > maxNesting {
		out = append(out, fmt.Sprintf("%s: func %s nests %d deep (max %d)",
			path, fn.Name.Name, depth, maxNesting))
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
		switch c := child.(type) {
		case *ast.FuncLit:
			deepest = max(deepest, nesting(c.Body, 0))
			return false
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
			*ast.TypeSwitchStmt, *ast.SelectStmt:
			deepest = max(deepest, nesting(c, depth+1))
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
	cb, err := clientcodebase.NewFromConfig(t.Context(), clientcodebase.Config{
		Kind: clientcodebase.KindFS,
		Root: filepath.Join("..", ".."),
	}, clientcodebase.Deps{})
	require.NoError(t, err)

	assert.Empty(t, sizeViolations(t, cb, budgetRoots()))
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
	long := "func Long() {\n" + strings.Repeat("\t_ = 1\n", 49) + "}\n"
	fits := "func Fits() {\n" + strings.Repeat("\t_ = 1\n", 48) + "}\n"
	deep := "func Deep() {\n" + strings.Repeat(
		"if true {\n",
		5,
	) + strings.Repeat(
		"}\n",
		5,
	) + "}\n"
	four := "func Four() {\n" + strings.Repeat(
		"if true {\n",
		4,
	) + strings.Repeat(
		"}\n",
		4,
	) + "}\n"
	cb, err := clientcodebase.NewFromConfig(t.Context(), clientcodebase.Config{},
		clientcodebase.Deps{Files: map[string]string{
			"budget/a.go": "package budget\n\n" + long + fits + deep + four,
		}})
	require.NoError(t, err)

	got := sizeViolations(t, cb, []string{"budget"})

	assert.Equal(t, []string{
		"budget/a.go: func Long is 51 lines (max 50)",
		"budget/a.go: func Deep nests 5 deep (max 4)",
	}, got)
}

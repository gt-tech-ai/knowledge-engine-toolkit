package codebase

import (
	"strconv"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/printer"
	"github.com/grafana/alloy/syntax/token"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// decodeAlloy decodes an Alloy (River) file into the shape decodeHCL produces: a block
// `a.b "label" { … }` is doc["a.b"]["label"], a list of bodies; an unlabeled block is
// doc["a.b"], a list of bodies; an attribute is its typed literal, or its source text for
// a reference, call or operator expression.
func decodeAlloy(src []byte, p string) (any, error) {
	file, err := parser.ParseFile(p, src)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "alloy "+p)
	}

	return alloyBody(file.Body), nil
}

// alloyBody converts a block body: attributes and nested blocks.
func alloyBody(body ast.Body) map[string]any {
	out := map[string]any{}

	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.AttributeStmt:
			out[s.Name.Name] = alloyExpr(s.Value)
		case *ast.BlockStmt:
			addAlloyBlock(out, s)
		}
	}

	return out
}

// addAlloyBlock appends a block to its name's list, under its label when it has one.
func addAlloyBlock(out map[string]any, s *ast.BlockStmt) {
	name := strings.Join(s.Name, ".")
	holder := out

	if s.Label != "" {
		labelled, _ := out[name].(map[string]any)
		if labelled == nil {
			labelled = map[string]any{}
			out[name] = labelled
		}

		holder, name = labelled, s.Label
	}

	bodies, _ := holder[name].([]any)

	holder[name] = append(bodies, alloyBody(s.Body))
}

// alloyExpr converts an expression: literals and composites to values, anything else to
// its printed source.
func alloyExpr(expr ast.Expr) any {
	switch e := expr.(type) {
	case *ast.LiteralExpr:
		return alloyLiteral(e)
	case *ast.ArrayExpr:
		return alloyArray(e)
	case *ast.ObjectExpr:
		return alloyObject(e)
	case *ast.ParenExpr:
		return alloyExpr(e.Inner)
	default:
		return alloySource(expr)
	}
}

// alloyArray converts each element.
func alloyArray(e *ast.ArrayExpr) []any {
	out := make([]any, 0, len(e.Elements))
	for _, el := range e.Elements {
		out = append(out, alloyExpr(el))
	}

	return out
}

// alloyObject converts each field.
func alloyObject(e *ast.ObjectExpr) map[string]any {
	out := make(map[string]any, len(e.Fields))
	for _, f := range e.Fields {
		out[f.Name.Name] = alloyExpr(f.Value)
	}

	return out
}

// alloySource renders an expression with no static value as its source text.
func alloySource(expr ast.Expr) string {
	var b strings.Builder

	err := printer.Fprint(&b, expr)
	if err != nil {
		return ""
	}

	return b.String()
}

// alloyLiteral converts a literal by its token kind; an unparsable literal is its text.
func alloyLiteral(e *ast.LiteralExpr) any {
	switch e.Kind {
	case token.STRING:
		s, err := strconv.Unquote(e.Value)
		if err == nil {
			return s
		}
	case token.NUMBER:
		return alloyNumber(e.Value)
	case token.FLOAT:
		return alloyFloat(e.Value)
	case token.BOOL:
		return e.Value == "true"
	case token.NULL:
		return nil
	}

	return e.Value
}

// alloyNumber parses an integer, then a float, else keeps the source text.
func alloyNumber(value string) any {
	n, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		return n
	}

	return alloyFloat(value)
}

// alloyFloat parses a float, else keeps the source text.
func alloyFloat(value string) any {
	f, err := strconv.ParseFloat(value, 64)
	if err == nil {
		return f
	}

	return value
}

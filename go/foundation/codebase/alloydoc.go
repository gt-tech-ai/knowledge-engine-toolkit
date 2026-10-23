package codebase

import (
	"strconv"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/printer"
	"github.com/grafana/alloy/syntax/token"
)

// decodeAlloy decodes an Alloy (River) file into the shape decodeHCL produces: a block
// `a.b "label" { … }` is doc["a.b"]["label"], a list of bodies; an unlabeled block is
// doc["a.b"], a list of bodies; an attribute is its typed literal, or its source text for
// a reference, call or operator expression.
func decodeAlloy(src []byte, p string) (any, error) {
	file, err := parser.ParseFile(p, src)
	if err != nil {
		return nil, err
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
	}
	return out
}

// alloyExpr converts an expression: literals and composites to values, anything else to
// its printed source.
func alloyExpr(expr ast.Expr) any {
	switch e := expr.(type) {
	case *ast.LiteralExpr:
		return alloyLiteral(e)
	case *ast.ArrayExpr:
		out := make([]any, 0, len(e.Elements))
		for _, el := range e.Elements {
			out = append(out, alloyExpr(el))
		}
		return out
	case *ast.ObjectExpr:
		out := make(map[string]any, len(e.Fields))
		for _, f := range e.Fields {
			out[f.Name.Name] = alloyExpr(f.Value)
		}
		return out
	case *ast.ParenExpr:
		return alloyExpr(e.Inner)
	default:
		var b strings.Builder
		if err := printer.Fprint(&b, expr); err != nil {
			return ""
		}
		return b.String()
	}
}

// alloyLiteral converts a literal by its token kind; an unparsable literal is its text.
func alloyLiteral(e *ast.LiteralExpr) any {
	switch e.Kind {
	case token.STRING:
		if s, err := strconv.Unquote(e.Value); err == nil {
			return s
		}
	case token.NUMBER:
		if n, err := strconv.ParseInt(e.Value, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(e.Value, 64); err == nil {
			return f
		}
	case token.FLOAT:
		if f, err := strconv.ParseFloat(e.Value, 64); err == nil {
			return f
		}
	case token.BOOL:
		return e.Value == "true"
	case token.NULL:
		return nil
	}
	return e.Value
}

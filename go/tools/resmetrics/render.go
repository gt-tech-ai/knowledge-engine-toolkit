package resmetrics

import (
	"bytes"
	"context"
	"embed"
	"go/format"
	"strconv"
	"strings"
	// The templates render Go and Python source, never HTML, so text/template is correct.
	"text/template" // nosemgrep: import-text-template

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// templates holds the two output templates.
//
//go:embed templates/*.tmpl
var templates embed.FS

// The rendered outputs' line widths: Go's lll limit and Python's ruff line-length.
const (
	// goWidth is the widest Go comment line, tab counted as one column.
	goWidth = 90

	// pyWidth is the widest Python docstring line.
	pyWidth = 90
)

// Render produces the Go and Python outputs from the taxonomy cb holds.
func (generator) Render(
	ctx context.Context,
	cb interfaces.Codebase,
) (types.Artifacts, error) {
	src, err := cb.Text(ctx, SourcePath)
	if err != nil {
		return nil, err
	}

	current, err := parse(src.Bytes)
	if err != nil {
		return nil, err
	}

	goSrc, err := execute("series.go.tmpl", current)
	if err != nil {
		return nil, err
	}

	goSrc, err = format.Source(goSrc)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "format "+GoPath)
	}

	pySrc, err := execute("series.py.tmpl", current)
	if err != nil {
		return nil, err
	}

	// The template's range blocks leave blank lines at the end; ruff wants exactly one
	// newline.
	pySrc = append(bytes.TrimRight(pySrc, "\n"), '\n')

	return types.Artifacts{GoPath: goSrc, PythonPath: pySrc}, nil
}

// execute renders the named embedded template over the taxonomy.
func execute(name string, current taxonomy) ([]byte, error) {
	currentTemplate, err := template.New(name).Funcs(template.FuncMap{
		"camel":  camel,
		"field":  field,
		"snake":  snake,
		"upper":  strings.ToUpper,
		"number": func(f *float64) string { return strconv.FormatFloat(*f, 'g', -1, 64) },
		"goComment": func(indent, text string) string {
			return wrap(indent+"// ", goWidth, text)
		},
		"pyDoc": func(indent, text string) string { return wrap(indent, pyWidth, text) },
	}).ParseFS(templates, "templates/"+name)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "parse template "+name)
	}

	var buf bytes.Buffer

	err = currentTemplate.Execute(&buf, current)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "render "+name)
	}

	return buf.Bytes(), nil
}

// camel turns a snake_case name into CamelCase: "half_open" → "HalfOpen".
func camel(s string) string {
	parts := strings.Split(s, "_")
	for index, part := range parts {
		if part != "" {
			parts[index] = strings.ToUpper(part[:1]) + part[1:]
		}
	}

	return strings.Join(parts, "")
}

// field is a series' Recorder field name: its metric name without the resilience_
// prefix and _total suffix, in lowerCamelCase ("resilience_retries_total" →
// "retries").
func field(name string) string {
	c := camel(strings.TrimSuffix(strings.TrimPrefix(name, "resilience_"), "_total"))

	return strings.ToLower(c[:1]) + c[1:]
}

// snake turns a CamelCase Go method name into snake_case: "SetBreakerState" →
// "set_breaker_state".
func snake(s string) string {
	out := make([]rune, 0, len(s))

	for index, char := range s {
		if char >= 'A' && char <= 'Z' {
			if index > 0 {
				out = append(out, '_')
			}

			char += 'a' - 'A'
		}

		out = append(out, char)
	}

	return string(out)
}

// wrap fills text into lines of at most width columns, each starting with prefix
// (counted in the width), joined by newlines. A word longer than a line stands alone.
func wrap(prefix string, width int, text string) string {
	var lines []string

	line := prefix

	for word := range strings.FieldsSeq(text) {
		switch {
		case line == prefix:
			line += word
		case len(line)+1+len(word) > width:
			lines = append(lines, line)
			line = prefix + word
		default:
			line += " " + word
		}
	}

	return strings.Join(append(lines, line), "\n")
}

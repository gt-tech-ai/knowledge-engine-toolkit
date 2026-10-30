package errcodes

import (
	"bytes"
	"context"
	"embed"
	"go/format"
	"strconv"
	"strings"
	// The templates render Go and Python source, never HTML, so text/template is correct.
	"text/template" // nosemgrep: import-text-template

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
	codes, err := parse(src.Bytes)
	if err != nil {
		return nil, err
	}
	goSrc, err := execute("codes.go.tmpl", codes)
	if err != nil {
		return nil, err
	}
	if goSrc, err = format.Source(goSrc); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "format "+GoPath)
	}
	pySrc, err := execute("codes.py.tmpl", codes)
	if err != nil {
		return nil, err
	}
	return types.Artifacts{GoPath: goSrc, PythonPath: pySrc}, nil
}

// execute renders the named embedded template over codes.
func execute(name string, codes []code) ([]byte, error) {
	t, err := template.New(name).Funcs(template.FuncMap{
		"facts": facts,
		"upper": upperFirst,
		"goComment": func(indent, text string) string {
			return wrap(indent+"// ", goWidth, text)
		},
		"pyDoc": func(indent, text string) string { return wrap(indent, pyWidth, text) },
	}).ParseFS(templates, "templates/"+name)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "parse template "+name)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, codes); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "render "+name)
	}
	return buf.Bytes(), nil
}

// facts is a row's transport and retry facts as one clause: "HTTP 404, gRPC
// NOT_FOUND; permanent".
func facts(c code) string {
	retry := map[string]string{
		retryTransient: "transient",
		retryPermanent: "permanent",
		retryNone:      "neither transient nor permanent",
	}[c.Retry]
	return "HTTP " + strconv.Itoa(c.HTTP) + ", gRPC " + c.GRPC + "; " + retry
}

// upperFirst capitalizes s's first letter.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// wrap fills text into lines of at most width columns, each starting with prefix
// (counted in the width), joined by newlines. A word longer than a line stands alone.
func wrap(prefix string, width int, text string) string {
	var lines []string
	line := prefix
	for _, w := range strings.Fields(text) {
		switch {
		case line == prefix:
			line += w
		case len(line)+1+len(w) > width:
			lines = append(lines, line)
			line = prefix + w
		default:
			line += " " + w
		}
	}
	return strings.Join(append(lines, line), "\n")
}

// Package scaffold plans and applies the files a template set generates into a
// codebase, honoring each file's policy (written once, regenerated, or regenerated
// above a preserve marker).
package scaffold

import (
	"bytes"
	"context"
	iofs "io/fs"
	"strings"
	// The templates render source and config files, never HTML, so text/template is correct.
	"text/template" // nosemgrep: import-text-template

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// PreserveMarker marks the line from which a PolicyPreserve file keeps its existing
// content.
const PreserveMarker = "scaffold:preserve"

// tmplSuffix is stripped from a template file's name to get its output path.
const tmplSuffix = ".tmpl"

// Plan renders every file of tmpl with data (paths are templates too, and a `.tmpl`
// suffix is dropped) and decides each one's action against cb under policy. An entry
// is FileCreate when cb lacks the file, FileKeep when the result equals it (and always
// under PolicyOnce), FileUpdate otherwise, and FileConflict when a PolicyPreserve file
// or its template lacks the preserve marker.
func Plan(
	ctx context.Context,
	tmpl iofs.FS,
	data any,
	policy func(path string) types.FilePolicy,
	cb interfaces.Codebase,
) (types.FilePlan, error) {
	var plan types.FilePlan
	err := iofs.WalkDir(tmpl, ".", func(name string, d iofs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		path, err := render(name, strings.TrimSuffix(name, tmplSuffix), data)
		if err != nil {
			return err
		}
		src, err := iofs.ReadFile(tmpl, name)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "read template "+name)
		}
		content, err := render(name, string(src), data)
		if err != nil {
			return err
		}
		entry, err := decide(ctx, cb, string(path), content, policy(string(path)))
		if err != nil {
			return err
		}
		plan.Entries = append(plan.Entries, entry)
		return nil
	})
	if err != nil {
		return types.FilePlan{}, apperr.Wrap(err, apperr.Code(err), "scaffold plan")
	}
	return plan, nil
}

// render executes text as the template named name over data.
func render(name, text string, data any) ([]byte, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse template "+name)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "render template "+name)
	}
	return b.Bytes(), nil
}

// decide sets the entry's action from the existing file.
func decide(
	ctx context.Context,
	cb interfaces.Codebase,
	path string,
	content []byte,
	policy types.FilePolicy,
) (types.FileEntry, error) {
	e := types.FileEntry{Path: path, Content: content, Policy: policy}
	text, err := cb.Text(ctx, path)
	switch {
	case apperr.Is(err, apperr.CodeNotFound):
		e.Action = types.FileCreate
		return e, nil
	case err != nil:
		return e, err
	}
	have := text.Bytes
	switch policy {
	case types.PolicyOnce:
		e.Action, e.Content = types.FileKeep, have
		return e, nil
	case types.PolicyPreserve:
		merged, ok := preserve(content, have)
		if !ok {
			e.Action, e.Reason = types.FileConflict, "no "+PreserveMarker+" line"
			return e, nil
		}
		e.Content = merged
	case types.PolicyRegenerate:
	}
	e.Action = types.FileUpdate
	if bytes.Equal(e.Content, have) {
		e.Action = types.FileKeep
	}
	return e, nil
}

// preserve joins fresh's lines above its marker with have's lines from its marker
// down; false when either lacks the marker.
func preserve(fresh, have []byte) ([]byte, bool) {
	head, ok := cut(fresh)
	if !ok {
		return nil, false
	}
	at, ok := markerAt(have)
	if !ok {
		return nil, false
	}
	return append(head[:len(head):len(head)], have[at:]...), true
}

// cut returns src up to its marker line.
func cut(src []byte) ([]byte, bool) {
	at, ok := markerAt(src)
	return src[:at], ok
}

// markerAt is the offset of the start of src's first marker line.
func markerAt(src []byte) (int, bool) {
	i := bytes.Index(src, []byte(PreserveMarker))
	if i < 0 {
		return 0, false
	}
	return bytes.LastIndexByte(src[:i], '\n') + 1, true
}

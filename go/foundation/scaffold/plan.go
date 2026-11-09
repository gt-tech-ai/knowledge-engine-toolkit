// Package scaffold plans and applies the files a template set (Plan) or the caller's
// own rendering (PlanFiles) generates into a codebase, honoring each file's policy
// (written once, regenerated, or regenerated above a preserve marker).
package scaffold

import (
	"bytes"
	"context"
	iofs "io/fs"
	"maps"
	"slices"
	"strings"
	// The templates render source and config files, never HTML, so text/template is correct.
	"text/template" // nosemgrep: import-text-template

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
	codebase interfaces.Codebase,
) (types.FilePlan, error) {
	var plan types.FilePlan

	err := iofs.WalkDir(tmpl, ".", func(name string, d iofs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		entry, err := planFile(ctx, codebase, tmpl, name, data, policy)
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

// planFile renders one template and decides what applying it would do.
func planFile(
	ctx context.Context,
	codebase interfaces.Codebase,
	tmpl iofs.FS,
	name string,
	data any,
	policy func(path string) types.FilePolicy,
) (types.FileEntry, error) {
	path, content, err := renderFile(tmpl, name, data)
	if err != nil {
		return types.FileEntry{}, err
	}

	return decide(ctx, codebase, path, content, policy(path))
}

// renderFile renders one template's destination path and content.
func renderFile(
	tmpl iofs.FS,
	name string,
	data any,
) (target string, rendered []byte, retErr error) {
	path, err := render(name, strings.TrimSuffix(name, tmplSuffix), data)
	if err != nil {
		return "", nil, err
	}

	src, err := iofs.ReadFile(tmpl, name)
	if err != nil {
		return "", nil, apperr.Wrap(err, apperr.CodeInternal, "read template "+name)
	}

	content, err := render(name, string(src), data)
	if err != nil {
		return "", nil, err
	}

	return string(path), content, nil
}

// PlanFiles decides each of files' actions against cb under policy, as Plan does for a
// template set, for content the caller rendered itself. Entries are in path order.
func PlanFiles(
	ctx context.Context,
	codebase interfaces.Codebase,
	files types.Artifacts,
	policy func(path string) types.FilePolicy,
) (types.FilePlan, error) {
	var plan types.FilePlan

	for _, path := range slices.Sorted(maps.Keys(files)) {
		entry, err := decide(ctx, codebase, path, files[path], policy(path))
		if err != nil {
			return types.FilePlan{}, apperr.Wrap(err, apperr.Code(err), "scaffold plan")
		}

		plan.Entries = append(plan.Entries, entry)
	}

	return plan, nil
}

// render executes text as the template named name over data.
func render(name, text string, data any) ([]byte, error) {
	templateValue, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "parse template "+name)
	}

	var buffer bytes.Buffer

	err = templateValue.Execute(&buffer, data)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInvalidInput, "render template "+name)
	}

	return buffer.Bytes(), nil
}

// decide sets the entry's action from the existing file.
func decide(
	ctx context.Context,
	codebase interfaces.Codebase,
	path string,
	content []byte,
	policy types.FilePolicy,
) (types.FileEntry, error) {
	fileEntry := types.FileEntry{Path: path, Content: content, Policy: policy}
	text, err := codebase.Text(ctx, path)

	switch {
	case apperr.Is(err, apperr.CodeNotFound):
		fileEntry.Action = types.FileCreate

		return fileEntry, nil
	case err != nil:
		return fileEntry, err
	}

	have := text.Bytes

	switch policy {
	case types.PolicyOnce:
		fileEntry.Action, fileEntry.Content = types.FileKeep, have

		return fileEntry, nil
	case types.PolicyPreserve:
		merged, ok := preserve(content, have)
		if !ok {
			fileEntry.Action, fileEntry.Reason = types.FileConflict, "no "+PreserveMarker+" line"

			return fileEntry, nil
		}

		fileEntry.Content = merged
	case types.PolicyRegenerate:
		fileEntry.Content = content
	default:
		// An unlisted policy regenerates too: e.Content already holds content.
	}

	fileEntry.Action = types.FileUpdate
	if bytes.Equal(fileEntry.Content, have) {
		fileEntry.Action = types.FileKeep
	}

	return fileEntry, nil
}

// preserve joins fresh's lines above its marker with have's lines from its marker
// down; false when either lacks the marker.
func preserve(fresh, have []byte) ([]byte, bool) {
	head, found := cut(fresh)
	if !found {
		return nil, false
	}

	at, found := markerAt(have)
	if !found {
		return nil, false
	}

	return append(slices.Clip(head), have[at:]...), true
}

// cut returns src up to its marker line.
func cut(src []byte) ([]byte, bool) {
	at, ok := markerAt(src)

	return src[:at], ok
}

// markerAt is the offset of the start of src's first marker line.
func markerAt(src []byte) (int, bool) {
	before, _, ok := bytes.Cut(src, []byte(PreserveMarker))
	if !ok {
		return 0, false
	}

	return bytes.LastIndexByte(before, '\n') + 1, true
}

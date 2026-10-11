// Package file implements the file-backed credential Source: it reads and writes
// credentials in a git-ignored local file (a dotenv-style KEY=value store) named by a
// caller-supplied Ref→location mapping. It is the read/write backend of clients/secrets,
// selected by KindFile. Put refuses to write to a path that git does not ignore — via an
// injected CommandRunner running `git check-ignore` — so a credential can never be
// committed by accident.
package file

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// credentialFilePerm is the mode for a written credential file: owner read/write only,
// so a secret store is never group- or world-readable.
const credentialFilePerm os.FileMode = 0o600

// Location addresses a credential within a git-ignored file: the file path and the KEY
// within that file's KEY=value lines.
type Location struct {
	// Path is the filesystem path of the git-ignored credential file.
	Path string
	// Key is the KEY within that file's KEY=value lines.
	Key string
}

// source reads and writes credentials from git-ignored files named by a caller-supplied
// Ref→location mapping; runner runs `git check-ignore` to gate writes.
type source struct {
	// locs maps a credential Ref to the file + key that holds its value.
	locs map[types.Ref]Location
	// runner runs `git check-ignore` so Put refuses a non-ignored path.
	runner interfaces.CommandRunner
}

// New builds a file-backed Source over the given Ref→location mapping. runner runs the
// `git check-ignore` write guard and must be non-nil.
func New(locs map[types.Ref]Location, runner interfaces.CommandRunner) interfaces.Source {
	return source{locs: locs, runner: runner}
}

// Get resolves ref to its file location and returns the value of its key as an opaque
// Secret. An unmapped ref, a missing file, or a missing/empty key is a coded not-found
// error.
func (s source) Get(_ context.Context, ref types.Ref) (types.Secret, error) {
	loc, ok := s.locs[ref]
	if !ok {
		return types.Secret{}, apperr.NotFound(
			fmt.Sprintf("no file location mapped for credential %s", ref),
		)
	}
	data, err := os.ReadFile(loc.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return types.Secret{}, apperr.NotFound(
				fmt.Sprintf("credential file %q does not exist for %s", loc.Path, ref),
			)
		}
		return types.Secret{}, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"reading credential file",
		)
	}
	value, found := lookup(data, loc.Key)
	if !found || value == "" {
		return types.Secret{}, apperr.NotFound(
			fmt.Sprintf("key %q not found in credential file for %s", loc.Key, ref),
		)
	}
	return types.NewSecret(value), nil
}

// Put writes value under ref's key, but only after `git check-ignore` confirms the file
// path is git-ignored — otherwise it refuses with a coded error so a credential can
// never be committed. The raw value is revealed here (the legitimate injection boundary:
// persisting to the git-ignored store) and nowhere else.
func (s source) Put(ctx context.Context, ref types.Ref, value types.Secret) error {
	loc, ok := s.locs[ref]
	if !ok {
		return apperr.NotFound(
			fmt.Sprintf("no file location mapped for credential %s", ref),
		)
	}
	// Ensure the parent directory exists (0700 — a secrets dir), so a first write to a
	// not-yet-created git-ignored store (e.g. .secrets/) succeeds; the check-ignore below
	// also runs with this directory as its working dir.
	if err := os.MkdirAll(filepath.Dir(loc.Path), 0o700); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "creating credential directory")
	}
	// `git check-ignore -q <path>` exits 0 when the path IS ignored; the CommandRunner
	// returns a non-nil error for any non-zero exit (not-ignored, or git unavailable),
	// so we fail SAFE — refuse the write unless git positively confirms the path ignored.
	if err := s.runner.Run(
		ctx,
		filepath.Dir(loc.Path),
		"git",
		"check-ignore",
		"-q",
		loc.Path,
	); err != nil {
		return apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf(
				"refusing to write credential to non-git-ignored path %q",
				loc.Path,
			),
		)
	}

	data, err := os.ReadFile(loc.Path)
	if err != nil && !os.IsNotExist(err) {
		return apperr.Wrap(err, apperr.CodeInternal, "reading credential file for update")
	}
	updated := upsert(data, loc.Key, value.Reveal())
	if err := os.WriteFile(loc.Path, updated, credentialFilePerm); err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "writing credential file")
	}
	return nil
}

// lookup returns the value for key in a dotenv-style KEY=value buffer. Blank lines and
// lines beginning with '#' are ignored; the value is everything after the first '='.
func lookup(data []byte, key string) (string, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := splitKV(line)
		if ok && k == key {
			return v, true
		}
	}
	return "", false
}

// upsert returns data with key set to value: it replaces the first matching KEY= line,
// or appends a new one when the key is absent.
func upsert(data []byte, key, value string) []byte {
	line := key + "=" + value
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	replaced := false
	out := make([]string, 0, len(lines)+1)
	for _, l := range lines {
		if k, _, ok := splitKV(l); ok && k == key {
			// Preserve a shell `export ` prefix on the replaced line, so updating a key in a
			// dotenv file maintained for both `source` and this backend keeps it sourceable.
			if strings.HasPrefix(strings.TrimSpace(l), "export ") {
				out = append(out, "export "+line)
			} else {
				out = append(out, line)
			}
			replaced = true
			continue
		}
		out = append(out, l)
	}
	if !replaced {
		out = append(out, line)
	}
	// Drop a leading empty element from splitting an empty/whitespace buffer.
	if len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// splitKV parses a single dotenv line into its key and value. It reports ok=false for a
// blank line or a comment (leading '#'), or a line without '='. A leading `export ` is
// tolerated (`export KEY=value`) so a git-ignored dotenv file maintained for BOTH shell
// `source` and this backend parses identically — the key is matched without the prefix,
// and a matching pair of surrounding single/double quotes on the value (shell quoting,
// e.g. `KEY="value"`) is stripped so the value is the credential itself, not the quoted
// literal.
func splitKV(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	eq := strings.IndexByte(trimmed, '=')
	if eq < 0 {
		return "", "", false
	}
	return strings.TrimSpace(
			trimmed[:eq],
		), unquote(
			strings.TrimSpace(trimmed[eq+1:]),
		), true
}

// unquote strips one matching pair of surrounding single or double quotes from a dotenv
// value (shell quoting), leaving an unquoted or unbalanced value unchanged.
func unquote(v string) string {
	if len(v) >= 2 {
		if c := v[0]; (c == '"' || c == '\'') && v[len(v)-1] == c {
			return v[1 : len(v)-1]
		}
	}
	return v
}

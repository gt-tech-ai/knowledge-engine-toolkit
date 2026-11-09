package types

import (
	"bytes"
	"strings"
)

// binarySniffLen is how many leading bytes are searched for a NUL.
const binarySniffLen = 8192

// File is one file of a repository, named by its slash-separated path relative to the
// repository root.
type File struct {
	// Path is the repository-relative, slash-separated path.
	Path string

	// Untracked marks a file that is present on disk but not yet tracked by the
	// repository's version control (always false for a source without one).
	Untracked bool

	// Ignored marks an untracked file the repository's ignore rules exclude; only a
	// selector with IncludeIgnored lists one.
	Ignored bool
}

// FileSelector selects a subset of a repository's files. Every non-empty field narrows
// the selection; the zero selector selects every file except generated Go files.
type FileSelector struct {
	// Roots keeps files under any of these repository-relative directories.
	Roots []string `yaml:"roots" json:"roots,omitempty"`

	// Paths keeps exactly these repository-relative files.
	Paths []string `yaml:"paths" json:"paths,omitempty"`

	// Globs keeps files matching any of these patterns; `**` matches any number of path
	// segments and `*`, `?` and `[...]` match within one segment.
	Globs []string `yaml:"globs" json:"globs,omitempty"`

	// Exts keeps files whose name ends in one of these extensions (".go", ".yaml").
	Exts []string `yaml:"exts" json:"exts,omitempty"`

	// Exclude drops files matching any of these globs; an entry with no glob
	// metacharacter drops the file or directory it names and everything under it.
	Exclude []string `yaml:"exclude" json:"exclude,omitempty"`

	// TrackedOnly drops untracked files.
	TrackedOnly bool `yaml:"tracked_only" json:"tracked_only,omitempty"`

	// IncludeGenerated keeps generated Go files (a `Code generated … DO NOT EDIT.`
	// header, or a generated name such as `*.pb.go`), which are dropped by default.
	IncludeGenerated bool `yaml:"include_generated" json:"include_generated,omitempty"`

	// IncludeIgnored also lists the files the repository's ignore rules exclude (a
	// gitignored generated tier), looked up only under the selector's narrowest scope:
	// its Paths, else its Roots, else its globs' literal directory prefixes. A source
	// without ignore rules has none.
	IncludeIgnored bool `yaml:"include_ignored" json:"include_ignored,omitempty"`
}

// IsZero reports whether s sets no field, so it is the default selector (every file
// except generated Go files) rather than one a caller configured.
func (s FileSelector) IsZero() bool {
	return len(s.Roots) == 0 && len(s.Paths) == 0 && len(s.Globs) == 0 &&
		len(
			s.Exts,
		) == 0 && len(s.Exclude) == 0 && !s.TrackedOnly && !s.IncludeGenerated &&
		!s.IncludeIgnored
}

// Text is one file's contents.
type Text struct {
	// Path is the repository-relative path the bytes were read from.
	Path string

	// Bytes is the file's contents.
	Bytes []byte

	// Binary reports a NUL byte in the first 8 KiB, git's text/binary heuristic.
	Binary bool
}

// Lines returns the file's lines without their terminators; a trailing newline does not
// add an empty last line.
func (t Text) Lines() []string {
	s := strings.TrimSuffix(
		strings.ReplaceAll(string(t.Bytes), "\r\n", textNewline),
		textNewline,
	)
	if s == "" {
		return nil
	}

	return strings.Split(s, textNewline)
}

// IsBinary reports whether data holds a NUL byte in its first 8 KiB.
func IsBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), binarySniffLen)], 0) >= 0
}

// Hit is one line of a file that matched a pattern.
type Hit struct {
	// File is the repository-relative path of the matching file.
	File string

	// Text is the whole matching line.
	Text string

	// Pattern is the source of the pattern that matched.
	Pattern string

	// Line is the 1-based line number.
	Line int

	// Col is the 1-based byte column where the match starts.
	Col int
}

// Finding is one violation a check reports.
type Finding struct {
	// Check names the check that reported the finding.
	Check string `json:"check"`

	// File is the repository-relative path the finding is in; empty for a finding about
	// the repository as a whole.
	File string `json:"file,omitempty"`

	// Rule names the rule within the check that was violated.
	Rule string `json:"rule,omitempty"`

	// Message says what is wrong and, where it can, how to fix it.
	Message string `json:"message"`

	// Line is the 1-based line number; 0 when the finding has no line.
	Line int `json:"line,omitempty"`
}

const (
	// ParseImports parses the package clause and imports only.
	ParseImports ParseMode = iota

	// ParseComments parses the whole file, comments included.
	ParseComments
)

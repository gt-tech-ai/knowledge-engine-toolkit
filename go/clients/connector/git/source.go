// Package git is the ConnectorSource over a git work tree on a mounted filesystem: its
// tracked files plus the untracked files git does not ignore, each an object keyed by its
// path with the SHA-256 of its bytes as the ETag. It needs no credential, so a consumer
// decides which root a connector may read; the source never takes a path from a tenant.
package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
)

// defaultPageSize is the page size a non-positive ListPage limit gets.
const defaultPageSize = 1000

// tokenPrefix starts every continuation token; the rest is the last key a page examined.
const tokenPrefix = "v1:"

// head is the revision every listing is stamped with.
const head = "HEAD"

// worktreeSuffix marks the Revision of a file whose bytes differ from HEAD's, so a
// citation never claims the commit holds text only the working tree has.
const worktreeSuffix = "+worktree"

// Config names the work tree and paths a Source lists.
type Config struct {
	// Root is the work tree's directory; required.
	Root string

	// IncludeGlobs keeps only the paths matching at least one pattern; empty keeps all.
	IncludeGlobs []string

	// ExcludeGlobs drops the paths matching any pattern, after IncludeGlobs.
	ExcludeGlobs []string
}

// Source is the ConnectorSource over a git work tree. The first page (an empty token)
// takes one listing — the paths, HEAD and the files that differ from it — and later
// pages of the crawl reuse it; a token resumes after the last key it names, so a source
// that starts mid-crawl lists afresh without repeating or dropping a remaining path.
// Each regular file is one object: Key is its slash-separated path, ETag the hex SHA-256
// of its bytes, Size its byte size, LastModified its modification time and Revision HEAD
// (HEAD+worktree when it differs). Files are read through an os.Root, so no listed path
// reads outside the work tree; a symbolic link, and a file gone by the time its page is
// read, is skipped. Safe for concurrent use.
type Source struct {
	// git lists the work tree.
	git interfaces.GitClient

	// snap is the crawl's listing.
	snap *snapshot

	// root is the work tree's directory.
	root string

	// include and exclude are the configured globs.
	include, exclude []string

	// mu guards snap.
	mu sync.Mutex
}

// Compile-time assertion that Source satisfies ConnectorSource.
var _ interfaces.ConnectorSource = (*Source)(nil)

// snapshot is one listing of the work tree.
type snapshot struct {
	// worktree holds the listed paths whose bytes differ from HEAD's.
	worktree map[string]bool

	// head is HEAD's commit when listed.
	head string

	// paths are the listed paths that pass the globs, sorted.
	paths []string
}

// NewSource returns the Source for cfg over git. It does no I/O; an empty root is
// CodeInvalidInput.
func NewSource(cfg *Config, git interfaces.GitClient) (*Source, error) {
	if cfg.Root == "" {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"git: the work tree root is required",
		)
	}

	return &Source{
		git:     git,
		snap:    nil,
		root:    cfg.Root,
		include: cfg.IncludeGlobs,
		exclude: cfg.ExcludeGlobs,
		mu:      sync.Mutex{},
	}, nil
}

// TestConnection checks the root is a git work tree and returns how many paths a crawl
// of it would list.
func (s *Source) TestConnection(ctx context.Context) (int, error) {
	snap, err := s.list(ctx)
	if err != nil {
		return 0, err
	}

	return len(snap.paths), nil
}

// ListPage returns up to limit files after the key continuationToken names; an empty
// token takes a fresh listing and starts at the first path.
func (s *Source) ListPage(
	ctx context.Context,
	continuationToken string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	if limit <= 0 {
		limit = defaultPageSize
	}

	after, err := decodeToken(continuationToken)
	if err != nil {
		return nil, "", err
	}

	snap, err := s.listing(ctx, continuationToken)
	if err != nil {
		return nil, "", err
	}

	tree, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, "", apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"git: open the work tree",
		)
	}

	defer func() { _ = tree.Close() }()

	return page(tree, snap, after, limit)
}

// page reads up to limit objects of snap after the key after from tree, and the token
// that resumes after the last key it examined ("" when snap is exhausted).
func page(
	tree *os.Root,
	snap *snapshot,
	after string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	start := resume(snap.paths, after)
	objects := make([]interfaces.StorageObject, 0, min(limit, len(snap.paths)-start))

	for i := start; i < len(snap.paths); i++ {
		if len(objects) == limit {
			return objects, tokenPrefix + snap.paths[i-1], nil
		}

		obj, ok, err := object(tree, snap, snap.paths[i])
		if err != nil {
			return nil, "", err
		}

		if ok {
			objects = append(objects, obj)
		}
	}

	return objects, "", nil
}

// resume is the index of the first path after the key after in the sorted paths.
func resume(paths []string, after string) int {
	start, found := slices.BinarySearch(paths, after)
	if found {
		start++
	}

	return start
}

// listing is the crawl's snapshot: the held one for a continuation token, a fresh one
// for the first page or when none is held.
func (s *Source) listing(ctx context.Context, token string) (*snapshot, error) {
	if held := s.held(); held != nil && token != "" {
		return held, nil
	}

	snap, err := s.list(ctx)
	if err != nil {
		return nil, err
	}

	s.hold(snap)

	return snap, nil
}

// held is the snapshot the crawl holds, or nil.
func (s *Source) held() *snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.snap
}

// hold makes snap the crawl's snapshot.
func (s *Source) hold(snap *snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.snap = snap
}

// list lists the work tree: its tracked and untracked, non-ignored paths that pass the
// globs, HEAD, and the paths that differ from HEAD. A root that is not a work tree is
// CodeInvalidInput.
func (s *Source) list(ctx context.Context) (*snapshot, error) {
	if !s.git.IsRepo(ctx, s.root) {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"git: the root is not a git work tree",
		)
	}

	tracked, err := s.git.LsFiles(ctx, s.root, nil)
	if err != nil {
		return nil, err
	}

	untracked, err := s.git.LsUntracked(ctx, s.root, nil)
	if err != nil {
		return nil, err
	}

	commit, err := s.git.RevParse(ctx, s.root, head)
	if err != nil {
		return nil, err
	}

	changed, err := s.git.ChangedSince(ctx, s.root, head)
	if err != nil {
		return nil, err
	}

	worktree := make(map[string]bool, len(changed)+len(untracked))
	for _, path := range slices.Concat(changed, untracked) {
		worktree[path] = true
	}

	paths := slices.DeleteFunc(slices.Concat(tracked, untracked), func(path string) bool {
		return !s.keep(path)
	})
	slices.Sort(paths)

	return &snapshot{worktree: worktree, head: commit, paths: slices.Compact(paths)}, nil
}

// object reads path's object from tree; ok is false for a symbolic link, a non-regular
// file, or a file gone since the listing.
func object(
	tree *os.Root,
	snap *snapshot,
	path string,
) (obj interfaces.StorageObject, ok bool, err error) {
	info, err := tree.Lstat(path)
	if apperr.StdIs(err, fs.ErrNotExist) {
		return obj, false, nil
	}

	if err != nil {
		return obj, false, apperr.Wrap(
			err,
			apperr.CodeUnavailable,
			"git: stat a listed file",
		)
	}

	if !info.Mode().IsRegular() {
		return obj, false, nil
	}

	etag, err := contentHash(tree, path)
	if apperr.StdIs(err, fs.ErrNotExist) {
		return obj, false, nil
	}

	if err != nil {
		return obj, false, err
	}

	revision := snap.head
	if snap.worktree[path] {
		revision += worktreeSuffix
	}

	return interfaces.StorageObject{
		Key:          path,
		ContentType:  "",
		LastModified: info.ModTime().UTC().Format(time.RFC3339),
		ETag:         etag,
		Revision:     revision,
		Size:         info.Size(),
	}, true, nil
}

// contentHash is the hex SHA-256 of the file at path in tree.
func contentHash(tree *os.Root, path string) (string, error) {
	file, err := tree.Open(path)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeUnavailable, "git: open a listed file")
	}

	defer func() { _ = file.Close() }()

	sum := sha256.New()

	_, err = io.Copy(sum, file)
	if err != nil {
		return "", apperr.Wrap(err, apperr.CodeUnavailable, "git: read a listed file")
	}

	return hex.EncodeToString(sum.Sum(nil)), nil
}

// keep reports whether path passes the include globs (none: all) and no exclude glob.
func (s *Source) keep(path string) bool {
	matches := func(pattern string) bool { return codebase.Glob(pattern, path) }

	if len(s.include) > 0 && !slices.ContainsFunc(s.include, matches) {
		return false
	}

	return !slices.ContainsFunc(s.exclude, matches)
}

// decodeToken is the key a continuation token resumes after ("" for none); a token
// without the version prefix is CodeInvalidInput.
func decodeToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}

	after, ok := strings.CutPrefix(token, tokenPrefix)
	if !ok || after == "" {
		return "", apperr.New(
			apperr.CodeInvalidInput,
			"git: malformed continuation token",
		)
	}

	return after, nil
}

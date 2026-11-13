package github

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/codebase"
)

// defaultPageSize is the page size a non-positive ListPage limit gets.
const defaultPageSize = 1000

// blobType is a tree entry holding file content; trees and submodule commits are
// skipped.
const blobType = "blob"

// symlinkMode is a blob that is a symbolic link; its content is a path, not a document.
const symlinkMode = "120000"

// tokenVersion prefixes every continuation token, so a later format can be told apart.
const tokenVersion = "v1"

// pathSep separates a ref's segments and a continuation token's fields.
const pathSep = "/"

// Config names the repository, branch and paths a Source lists.
type Config struct {
	// BaseURL is the REST API root; empty means DefaultBaseURL.
	BaseURL string

	// Owner is the repository's owner login.
	Owner string

	// Repo is the repository's name.
	Repo string

	// Branch is the branch listed; empty means the repository's default branch.
	Branch string

	// IncludeGlobs keeps only the paths matching at least one pattern; empty keeps all.
	IncludeGlobs []string

	// ExcludeGlobs drops the paths matching any pattern, after IncludeGlobs.
	ExcludeGlobs []string
}

// Source is the ConnectorSource over one branch of a GitHub repository. The first page
// pins the branch head's commit, and every continuation token carries it, so a crawl
// lists one tree even when the branch moves mid-crawl. Each regular file is one object:
// Key is its path, ETag its blob SHA, Size its byte size and LastModified the pinned
// commit's committer date. Safe for concurrent use.
type Source struct {
	// tokens yields the installation token each request sends.
	tokens TokenSource

	// snap is the last tree listed, reused while a crawl stays on its commit.
	snap *snapshot

	// api sends the requests.
	api api

	// repoPath is /repos/<owner>/<repo>.
	repoPath string

	// branch is the configured branch ("" = the default branch).
	branch string

	// include and exclude are the configured globs.
	include, exclude []string

	// mu guards snap.
	mu sync.Mutex
}

// Compile-time assertion that Source satisfies ConnectorSource.
var _ interfaces.ConnectorSource = (*Source)(nil)

// snapshot is one commit's filtered, path-ordered file list.
type snapshot struct {
	// sha is the commit.
	sha string

	// files are the listed objects.
	files []interfaces.StorageObject
}

// NewSource returns the Source for cfg, authorizing each request with a token from
// tokens and sending it through doer. It does no I/O; a missing owner or repository is
// CodeInvalidInput.
func NewSource(
	cfg *Config,
	tokens TokenSource,
	doer interfaces.HTTPDoer,
) (*Source, error) {
	if cfg.Owner == "" || cfg.Repo == "" {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"github: the repository owner and name are required",
		)
	}

	return &Source{
		tokens: tokens,
		snap:   nil,
		api:    newAPI(doer, cfg.BaseURL),
		repoPath: "/repos/" + url.PathEscape(
			cfg.Owner,
		) + pathSep + url.PathEscape(
			cfg.Repo,
		),
		branch:  cfg.Branch,
		include: cfg.IncludeGlobs,
		exclude: cfg.ExcludeGlobs,
		mu:      sync.Mutex{},
	}, nil
}

// TestConnection resolves the branch head and returns how many files a crawl of it
// would list.
func (s *Source) TestConnection(ctx context.Context) (int, error) {
	pos, err := s.head(ctx)
	if err != nil {
		return 0, err
	}

	snap, err := s.tree(ctx, pos)
	if err != nil {
		return 0, err
	}

	return len(snap.files), nil
}

// ListPage returns up to limit files after continuationToken's offset in the commit it
// pins; an empty token resolves the branch head and starts at the first file.
func (s *Source) ListPage(
	ctx context.Context,
	continuationToken string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	if limit <= 0 {
		limit = defaultPageSize
	}

	pos, err := s.position(ctx, continuationToken)
	if err != nil {
		return nil, "", err
	}

	snap, err := s.tree(ctx, pos)
	if err != nil {
		return nil, "", err
	}

	if pos.offset > len(snap.files) {
		return nil, "", apperr.New(
			apperr.CodeInvalidInput,
			"github: the continuation token's offset is past the tree's end",
		)
	}

	end := min(pos.offset+limit, len(snap.files))
	page := slices.Clone(snap.files[pos.offset:end])

	if end == len(snap.files) {
		return page, "", nil
	}

	pos.offset = end

	return page, pos.encode(), nil
}

// cursor is a position in a crawl: the pinned commit, its committer date and the
// offset of the next file.
type cursor struct {
	// sha is the pinned commit.
	sha string

	// date is the commit's RFC 3339 committer date, every object's LastModified.
	date string

	// offset is the index of the next file.
	offset int
}

// position is where a page starts: token's cursor, or the branch head at offset zero
// for an empty token.
func (s *Source) position(ctx context.Context, token string) (cursor, error) {
	if token != "" {
		return decodeToken(token)
	}

	return s.head(ctx)
}

// repository is the slice of GET /repos/{owner}/{repo} the source reads.
type repository struct {
	// DefaultBranch is the repository's default branch.
	DefaultBranch string `json:"default_branch"`
}

// branchReply is the slice of GET /repos/{owner}/{repo}/branches/{branch} the source
// reads.
type branchReply struct {
	// Commit is the branch head.
	Commit branchHead `json:"commit"`
}

// branchHead is a branch's head commit.
type branchHead struct {
	// Commit is the head's git commit.
	Commit gitCommit `json:"commit"`

	// SHA is the head commit.
	SHA string `json:"sha"`
}

// gitCommit is the slice of a git commit the source reads.
type gitCommit struct {
	// Committer carries the commit date.
	Committer gitActor `json:"committer"`
}

// gitActor is a commit's committer.
type gitActor struct {
	// Date is when the commit was committed.
	Date time.Time `json:"date"`
}

// head resolves the branch (the default branch when none is configured) to a cursor at
// its head commit's first file.
func (s *Source) head(ctx context.Context) (cursor, error) {
	branch := s.branch
	if branch == "" {
		var repo repository

		err := s.get(ctx, s.repoPath, &repo)
		if err != nil {
			return cursor{}, err
		}

		branch = repo.DefaultBranch
	}

	var reply branchReply

	err := s.get(ctx, s.repoPath+"/branches/"+escapePath(branch), &reply)
	if err != nil {
		return cursor{}, err
	}

	return cursor{
		sha:    reply.Commit.SHA,
		date:   reply.Commit.Commit.Committer.Date.Format(time.RFC3339),
		offset: 0,
	}, nil
}

// treeReply is GET /repos/{owner}/{repo}/git/trees/{sha}?recursive=1.
type treeReply struct {
	// Tree is every entry, directories included.
	Tree []treeEntry `json:"tree"`

	// Truncated is set when GitHub cut the listing at its size limit.
	Truncated bool `json:"truncated"`
}

// treeEntry is one entry of a recursive tree.
type treeEntry struct {
	// Path is the entry's repository-relative path.
	Path string `json:"path"`

	// Mode is the git file mode (120000 for a symlink).
	Mode string `json:"mode"`

	// Type is blob, tree or commit (a submodule).
	Type string `json:"type"`

	// SHA is the object id.
	SHA string `json:"sha"`

	// Size is a blob's byte size.
	Size int64 `json:"size"`
}

// tree returns the cursor's commit's filtered file list, from the cache when the last
// crawl listed the same commit.
func (s *Source) tree(ctx context.Context, pos cursor) (*snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.snap != nil && s.snap.sha == pos.sha {
		return s.snap, nil
	}

	var reply treeReply

	path := s.repoPath + "/git/trees/" + url.PathEscape(pos.sha) + "?recursive=1"

	err := s.get(ctx, path, &reply)
	if err != nil {
		return nil, err
	}

	if reply.Truncated {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"github: the repository's tree exceeds GitHub's recursive tree limit;"+
				" list it with the git kind",
		)
	}

	s.snap = &snapshot{sha: pos.sha, files: s.files(reply.Tree, pos.date)}

	return s.snap, nil
}

// files are the regular files among entries that pass the globs, in path order, each
// dated date.
func (s *Source) files(entries []treeEntry, date string) []interfaces.StorageObject {
	files := make([]interfaces.StorageObject, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != blobType || entry.Mode == symlinkMode || !s.keep(entry.Path) {
			continue
		}

		files = append(files, interfaces.StorageObject{
			Key:          entry.Path,
			ContentType:  "",
			LastModified: date,
			ETag:         entry.SHA,
			Size:         entry.Size,
		})
	}

	slices.SortFunc(files, func(a, b interfaces.StorageObject) int {
		return cmp.Compare(a.Key, b.Key)
	})

	return files
}

// keep reports whether path passes the include globs (none: all) and no exclude glob.
func (s *Source) keep(path string) bool {
	matches := func(pattern string) bool { return codebase.Glob(pattern, path) }

	if len(s.include) > 0 && !slices.ContainsFunc(s.include, matches) {
		return false
	}

	return !slices.ContainsFunc(s.exclude, matches)
}

// get GETs path with the installation token and decodes the 200 reply into out.
func (s *Source) get(ctx context.Context, path string, out any) error {
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return err
	}

	return s.api.call(ctx, http.MethodGet, path, "Bearer "+token, http.StatusOK, out)
}

// escapePath escapes each segment of a slash-separated ref, keeping the slashes.
func escapePath(ref string) string {
	segments := strings.Split(ref, pathSep)
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}

	return strings.Join(segments, pathSep)
}

// encode is the continuation token for c: v1/<commit>/<offset>/<date>.
func (c cursor) encode() string {
	return tokenVersion + pathSep + c.sha + pathSep + strconv.Itoa(c.offset) + pathSep +
		c.date
}

// decodeToken reads a continuation token; a malformed one is CodeInvalidInput.
func decodeToken(token string) (cursor, error) {
	malformed := apperr.New(
		apperr.CodeInvalidInput,
		"github: malformed continuation token",
	)

	rest, found := strings.CutPrefix(token, tokenVersion+pathSep)
	if !found {
		return cursor{}, malformed
	}

	sha, rest, found := strings.Cut(rest, pathSep)
	if !found || sha == "" {
		return cursor{}, malformed
	}

	rawOffset, date, found := strings.Cut(rest, pathSep)
	if !found {
		return cursor{}, malformed
	}

	offset, err := strconv.Atoi(rawOffset)
	if err != nil || offset < 0 {
		return cursor{}, malformed
	}

	return cursor{sha: sha, date: date, offset: offset}, nil
}

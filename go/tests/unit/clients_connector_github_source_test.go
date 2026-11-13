package unit_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	connectorgh "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/github"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	ghfixtures "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/github"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// The github source tests' API paths, page size and file counts.
const (
	ghAPIBase      = "https://api.github.com"
	ghRepoPath     = "/repos/acme/widgets"
	ghBranchPath   = ghRepoPath + "/branches/main"
	ghTreePath     = ghRepoPath + "/git/trees/" + ghfixtures.HeadSHA
	ghToken        = "ghs_installation"
	ghPageSize     = 2
	ghRegularFiles = 6
	ghReadme       = "README.md"
	ghBigFileSize  = 50_000_000
)

// ghReply is one canned reply: a status and a fixture file (or a literal body).
type ghReply struct {
	header http.Header
	body   string
	status int
}

// ghOK is a 200 reply carrying the named fixture.
func ghOK(name string) ghReply {
	return ghReply{status: http.StatusOK, body: ghfixtures.Reply(name), header: nil}
}

// ghServe returns a doer that answers each request by its path (and, for a tree, its
// recursive query) from routes, failing the test on an unrouted request and on a
// request without the installation token. A route with several replies answers them in
// turn and repeats the last.
func ghServe(t *testing.T, routes map[string][]ghReply) *mocks.MockHTTPDoer {
	t.Helper()

	served := map[string]int{}
	doer := mocks.NewMockHTTPDoer(gomock.NewController(t))
	doer.EXPECT().Do(gomock.Any()).AnyTimes().
		DoAndReturn(func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "Bearer "+ghToken, req.Header.Get("Authorization"))

			key := req.URL.Path
			if req.URL.RawQuery != "" {
				key += "?" + req.URL.RawQuery
			}

			replies, ok := routes[key]
			require.True(t, ok, "unrouted request %s", key)

			reply := replies[min(served[key], len(replies)-1)]
			served[key]++

			resp := promResponse(reply.status, reply.body)

			resp.Header = reply.header

			return resp, nil
		})

	return doer
}

// ghRoutes is the happy path: main at HeadSHA and its whole tree.
func ghRoutes() map[string][]ghReply {
	return map[string][]ghReply{
		ghRepoPath:                  {ghOK("repo.json")},
		ghBranchPath:                {ghOK("branch.json")},
		ghTreePath + "?recursive=1": {ghOK("tree.json")},
	}
}

// ghSource builds the github source for acme/widgets over doer, with set applied to
// its config.
func ghSource(
	t *testing.T,
	doer interfaces.HTTPDoer,
	set func(*connectorgh.Config),
) *connectorgh.Source {
	t.Helper()

	tokens := mocks.NewMockGitHubTokenSource(gomock.NewController(t))
	tokens.EXPECT().Token(gomock.Any()).AnyTimes().Return(ghToken, nil)

	cfg := connectorgh.Config{
		BaseURL:      ghAPIBase,
		Owner:        "acme",
		Repo:         textWidgets,
		Branch:       "main",
		IncludeGlobs: nil,
		ExcludeGlobs: nil,
	}
	if set != nil {
		set(&cfg)
	}

	src, err := connectorgh.NewSource(&cfg, tokens, doer)
	require.NoError(t, err)

	return src
}

// ghKeys lists every page of src at limit and returns the keys in order.
func ghKeys(t *testing.T, src interfaces.ConnectorSource, limit int) []string {
	t.Helper()

	var keys []string

	token := ""

	for {
		objs, next, err := src.ListPage(t.Context(), token, limit)
		require.NoError(t, err)

		for _, obj := range objs {
			keys = append(keys, obj.Key)
		}

		if next == "" {
			return keys
		}

		token = next
	}
}

// TestGitHubSource_ListPagePagesTheTree tests that ListPage walks the branch head's
// recursive tree a page at a time.
//
// Why this test is important:
//   - The sync engine holds one page at a time and checkpoints the token; a listing
//     that skipped or repeated a file, or listed a directory, symlink or submodule as a
//     document, would ingest the wrong corpus.
//
// What it tests:
//   - Pages of two walk the six regular files in path order and end with an empty
//     token; each object carries the path as Key, the blob SHA as ETag, the size, the
//     pinned commit's date as LastModified and the pinned commit as Revision, so a
//     reader fetches the same file the listing saw. Directories, the symlink and the
//     submodule are not listed.
func TestGitHubSource_ListPagePagesTheTree(t *testing.T) {
	t.Parallel()

	src := ghSource(t, ghServe(t, ghRoutes()), nil)

	objs, next, err := src.ListPage(t.Context(), "", ghPageSize)
	require.NoError(t, err)
	require.NotEmpty(t, next)
	require.Len(t, objs, ghPageSize)
	assert.Equal(t, interfaces.StorageObject{
		Key:          ghReadme,
		ContentType:  "",
		LastModified: ghfixtures.CommitDate,
		ETag:         strings.Repeat("a1", 20),
		Revision:     ghfixtures.HeadSHA,
		Size:         120,
	}, objs[0])

	assert.Equal(t, []string{
		ghReadme, "big/data.bin", "docs/guide.md", "docs/img/logo.png",
		"go/main.go", "go/vendor/x.go",
	}, ghKeys(t, src, ghPageSize))
}

// TestGitHubSource_ResumeStaysOnOneCommit tests that a resumed listing reads the commit
// its first page pinned, even after the branch moves.
//
// Why this test is important:
//   - A push during a crawl would otherwise mix two trees: files deleted by the push
//     could be listed while files it added are skipped by an offset that shifted.
//
// What it tests:
//   - A fresh source resuming from the first page's token, with the branch now at
//     MovedSHA, lists the rest of HeadSHA's tree without asking for the branch; a
//     malformed token is CodeInvalidInput.
func TestGitHubSource_ResumeStaysOnOneCommit(t *testing.T) {
	t.Parallel()

	first := ghSource(t, ghServe(t, ghRoutes()), nil)

	_, token, err := first.ListPage(t.Context(), "", ghPageSize)
	require.NoError(t, err)

	moved := ghRoutes()

	moved[ghBranchPath] = []ghReply{ghOK("branch_moved.json")}

	resumed := ghSource(t, ghServe(t, moved), nil)

	objs, _, err := resumed.ListPage(t.Context(), token, ghPageSize)
	require.NoError(t, err)
	require.Len(t, objs, ghPageSize)
	assert.Equal(t, "docs/guide.md", objs[0].Key)

	_, _, err = resumed.ListPage(t.Context(), "not-a-token", ghPageSize)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestGitHubSource_GlobsFilter tests that the include and exclude globs pick the
// listed files before paging.
//
// Why this test is important:
//   - A connector indexes a slice of a repository (its docs and code, not vendored
//     code); filtering after paging would leave short or empty pages and wrong counts.
//
// What it tests:
//   - Include docs/** and **/*.go, exclude go/vendor/**: three files, in full pages.
func TestGitHubSource_GlobsFilter(t *testing.T) {
	t.Parallel()

	src := ghSource(t, ghServe(t, ghRoutes()), func(cfg *connectorgh.Config) {
		cfg.IncludeGlobs = []string{"docs/**", "**/*.go"}
		cfg.ExcludeGlobs = []string{"go/vendor/**"}
	})

	objs, next, err := src.ListPage(t.Context(), "", ghPageSize)
	require.NoError(t, err)
	assert.Len(t, objs, ghPageSize)
	assert.NotEmpty(t, next)

	assert.Equal(t,
		[]string{"docs/guide.md", "docs/img/logo.png", "go/main.go"},
		ghKeys(t, src, ghPageSize))
}

// TestGitHubSource_OversizedFilesListedWithSize tests that a large file is listed with
// its size rather than dropped.
//
// Why this test is important:
//   - The sync engine owns the oversized-file skip and reports it; a lister that
//     dropped big files silently would hide them from that report.
//
// What it tests:
//   - The 50 MB big/data.bin is listed with Size 50,000,000.
func TestGitHubSource_OversizedFilesListedWithSize(t *testing.T) {
	t.Parallel()

	src := ghSource(t, ghServe(t, ghRoutes()), nil)

	objs, _, err := src.ListPage(t.Context(), "", ghRegularFiles)
	require.NoError(t, err)
	require.Len(t, objs, ghRegularFiles)
	assert.Equal(t, "big/data.bin", objs[1].Key)
	assert.Equal(t, int64(ghBigFileSize), objs[1].Size)
}

// TestGitHubSource_MissingBranchIsInvalidInput tests that a branch GitHub does not know
// fails as a terminal input error.
//
// Why this test is important:
//   - A mistyped branch never heals; coding it as transient would retry the sync
//     forever instead of telling the operator.
//
// What it tests:
//   - A 404 for the branch is CodeInvalidInput from TestConnection and ListPage.
func TestGitHubSource_MissingBranchIsInvalidInput(t *testing.T) {
	t.Parallel()

	routes := ghRoutes()

	routes[ghBranchPath] = []ghReply{{
		status: http.StatusNotFound,
		body:   `{"message":"Branch not found"}`,
		header: nil,
	}}

	src := ghSource(t, ghServe(t, routes), nil)

	_, err := src.TestConnection(t.Context())
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, _, err = src.ListPage(t.Context(), "", ghPageSize)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestGitHubSource_ServerErrorAndRateLimitAreUnavailable tests that GitHub's transient
// failures are coded for retry.
//
// Why this test is important:
//   - The client tier retries only transient codes; a 5xx or a rate limit coded as
//     terminal would fail a sync that a later attempt completes.
//
// What it tests:
//   - A 502 for the branch, and a 403 carrying Retry-After (a secondary rate limit),
//     are CodeUnavailable.
func TestGitHubSource_ServerErrorAndRateLimitAreUnavailable(t *testing.T) {
	t.Parallel()

	for _, reply := range []ghReply{
		{status: http.StatusBadGateway, body: "bad gateway", header: nil},
		{
			status: http.StatusForbidden,
			body:   `{"message":"You have exceeded a secondary rate limit."}`,
			header: http.Header{"Retry-After": []string{"60"}},
		},
	} {
		routes := ghRoutes()

		routes[ghBranchPath] = []ghReply{reply}

		src := ghSource(t, ghServe(t, routes), nil)

		_, err := src.TestConnection(t.Context())
		assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err), reply.status)
	}
}

// TestGitHubSource_TruncatedTreeIsCoded tests that a tree GitHub truncated is refused.
//
// Why this test is important:
//   - GitHub caps a recursive tree's size; listing a truncated tree would silently
//     index part of the repository and later delete the rest as removed.
//
// What it tests:
//   - truncated: true is CodeInvalidInput (permanent: the repository exceeds the
//     recursive tree API's limit) from ListPage and TestConnection.
func TestGitHubSource_TruncatedTreeIsCoded(t *testing.T) {
	t.Parallel()

	routes := ghRoutes()

	routes[ghTreePath+"?recursive=1"] = []ghReply{ghOK("tree_truncated.json")}

	src := ghSource(t, ghServe(t, routes), nil)

	_, _, err := src.ListPage(t.Context(), "", ghPageSize)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	_, err = src.TestConnection(t.Context())
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestGitHubSource_TestConnectionCountsFiles tests that the probe resolves the default
// branch and counts the files a sync would list.
//
// Why this test is important:
//   - The connector form shows the probe's count before the first sync; a count that
//     included directories or ignored the globs would mislead the operator.
//
// What it tests:
//   - With no branch configured, the repository's default branch (main) is resolved
//     and its six regular files are counted.
func TestGitHubSource_TestConnectionCountsFiles(t *testing.T) {
	t.Parallel()

	src := ghSource(t, ghServe(t, ghRoutes()), func(cfg *connectorgh.Config) {
		cfg.Branch = ""
	})

	count, err := src.TestConnection(t.Context())
	require.NoError(t, err)
	assert.Equal(t, ghRegularFiles, count)
}

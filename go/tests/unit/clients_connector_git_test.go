package unit_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector"
	connectorgit "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/git"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// gitHead is the HEAD commit the mocked work tree reports.
const gitHead = "0123456789abcdef0123456789abcdef01234567"

// gitWorktreeRevision is the Revision of a file that differs from HEAD.
const gitWorktreeRevision = gitHead + "+worktree"

const (
	// gitB, gitC and gitEdited are file paths in the mocked work trees.
	gitB, gitC, gitEdited = "b.md", "c.md", "edited.md"

	// gitMain is a Go file in the mocked work trees.
	gitMain = "main.go"

	// gitFirst is a file's first content.
	gitFirst = "first"

	// gitProbes is how many probes the not-a-work-tree test makes.
	gitProbes = 2

	// gitKept is how many files the TestConnection tree keeps after its exclude.
	gitKept = 2
)

// gitTree is a mocked work tree: what git reports about it and the bytes on disk.
type gitTree struct {
	// files are the files written under the root, path to content.
	files map[string]string

	// tracked are the paths `git ls-files` reports.
	tracked []string

	// untracked are the untracked, non-ignored paths git reports.
	untracked []string

	// changed are the tracked paths that differ from HEAD.
	changed []string
}

// gitSource writes tree under a temp root and returns the root and a git Source over
// it whose GitClient reports tree as a work tree at gitHead.
func gitSource(
	t *testing.T,
	tree *gitTree,
	include, exclude []string,
) (string, *connectorgit.Source) {
	t.Helper()

	root := t.TempDir()

	src, err := connectorgit.NewSource(&connectorgit.Config{
		Root:         root,
		IncludeGlobs: include,
		ExcludeGlobs: exclude,
	}, gitMock(t, root, tree))
	require.NoError(t, err)

	return root, src
}

// gitMock writes tree under root and returns a GitClient that reports it as a work tree
// at gitHead.
func gitMock(t *testing.T, root string, tree *gitTree) *mocks.MockGitClient {
	t.Helper()

	for path, body := range tree.files {
		gitWrite(t, root, path, body)
	}

	git := mocks.NewMockGitClient(gomock.NewController(t))
	git.EXPECT().IsRepo(gomock.Any(), root).Return(true).AnyTimes()
	git.EXPECT().LsFiles(gomock.Any(), root, gomock.Nil()).Return(tree.tracked, nil).
		AnyTimes()
	git.EXPECT().LsUntracked(gomock.Any(), root, gomock.Nil()).
		Return(tree.untracked, nil).AnyTimes()
	git.EXPECT().RevParse(gomock.Any(), root, "HEAD").Return(gitHead, nil).AnyTimes()
	git.EXPECT().ChangedSince(gomock.Any(), root, "HEAD").Return(tree.changed, nil).
		AnyTimes()

	return git
}

// gitWrite writes body to path under root, creating its folders.
func gitWrite(t *testing.T, root, path, body string) {
	t.Helper()

	full := filepath.Join(root, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), mkdirAllInputFiles))
	require.NoError(t, os.WriteFile(full, []byte(body), fileModeInput))
}

// gitObjects crawls src with page size limit and returns every object, by key.
func gitObjects(
	t *testing.T,
	src interfaces.ConnectorSource,
	limit int,
) map[string]interfaces.StorageObject {
	t.Helper()

	objects := map[string]interfaces.StorageObject{}
	token := ""

	for {
		page, next, err := src.ListPage(context.Background(), token, limit)
		require.NoError(t, err)

		for _, obj := range page {
			objects[obj.Key] = obj
		}

		if next == "" {
			return objects
		}

		token = next
	}
}

// gitKeys returns the keys of objects.
func gitKeys(objects map[string]interfaces.StorageObject) []string {
	keys := make([]string, 0, len(objects))
	for key := range objects {
		keys = append(keys, key)
	}

	return keys
}

// TestGitSource_ListsTrackedAndUntracked tests that the git lister lists a work tree's
// tracked files and its untracked, non-ignored files, and nothing else on disk.
//
// Why this test is important:
//   - A new doc must be indexed before it is committed, while an ignored .venv or a
//     tracked symlink (its content is a path) must never be; and a citation must not
//     claim HEAD holds text only the working tree has.
//
// What it tests:
//   - Tracked a.md and untracked b.md are listed; the ignored file git does not report
//     and the tracked symlink are not. An unchanged tracked file's Revision is HEAD; an
//     edited tracked file's and an untracked file's are HEAD+worktree.
func TestGitSource_ListsTrackedAndUntracked(t *testing.T) {
	t.Parallel()

	tree := gitTree{
		files: map[string]string{
			textAMd: textA, gitEdited: "e", gitB: textB, ".venv/lib.py": "x",
		},
		tracked:   []string{textAMd, gitEdited, "link.md"},
		untracked: []string{gitB},
		changed:   []string{gitEdited},
	}
	root, src := gitSource(t, &tree, nil, nil)

	require.NoError(
		t,
		os.Symlink(filepath.Join(root, textAMd), filepath.Join(root, "link.md")),
	)

	objects := gitObjects(t, src, 0)

	assert.ElementsMatch(t, []string{textAMd, gitEdited, gitB}, gitKeys(objects))
	assert.Equal(t,
		[]string{gitHead, gitWorktreeRevision, gitWorktreeRevision},
		[]string{
			objects[textAMd].Revision, objects[gitEdited].Revision,
			objects[gitB].Revision,
		},
	)
}

// TestGitSource_GlobsFilter tests that the include and exclude globs apply before
// paging.
//
// Why this test is important:
//   - The dev connector's scope is the docs; generated and local files must never
//     reach a page, or the sync would ingest them.
//
// What it tests:
//   - Include docs/** and **/*.md, exclude docs/local/** → only docs/guide.md and
//     README.md are listed.
func TestGitSource_GlobsFilter(t *testing.T) {
	t.Parallel()

	tree := gitTree{
		files: map[string]string{
			ghGuide: "g", "docs/local/plan.md": "p", ghReadme: "r",
			gitMain: "m",
		},
		tracked:   []string{ghGuide, "docs/local/plan.md", ghReadme, gitMain},
		untracked: nil,
		changed:   nil,
	}
	_, src := gitSource(
		t,
		&tree,
		[]string{"docs/**", "**/*.md"},
		[]string{"docs/local/**"},
	)

	assert.ElementsMatch(t, []string{ghGuide, ghReadme},
		gitKeys(gitObjects(t, src, 1)))
}

// TestGitSource_ETagIsContentHash tests that each file's ETag is the SHA-256 of its
// bytes and its Size its byte size.
//
// Why this test is important:
//   - Untracked files have no blob SHA, so the content hash is what the sync's ETag
//     diff compares: stable across listings it skips the file, changed by an edit it
//     re-ingests it. The size is what the engine's oversized skip reads.
//
// What it tests:
//   - Two listings give the same ETag, the hex SHA-256 of the bytes, and the byte
//     size; after an edit the next listing's ETag is the new bytes' hash.
func TestGitSource_ETagIsContentHash(t *testing.T) {
	t.Parallel()

	tree := gitTree{
		files:     map[string]string{textAMd: gitFirst},
		tracked:   []string{textAMd},
		untracked: nil,
		changed:   nil,
	}
	root, src := gitSource(t, &tree, nil, nil)

	first := gitObjects(t, src, 0)[textAMd]

	assert.Equal(t, gitSHA256(gitFirst), first.ETag)
	assert.Equal(t, int64(len(gitFirst)), first.Size)
	assert.Equal(t, first.ETag, gitObjects(t, src, 0)[textAMd].ETag)

	gitWrite(t, root, textAMd, "second edit")

	assert.Equal(t, gitSHA256("second edit"), gitObjects(t, src, 0)[textAMd].ETag)
}

// gitSHA256 is the hex SHA-256 of body.
func gitSHA256(body string) string {
	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

// TestGitSource_PagesSkipAVanishedFile tests that a crawl pages through one listing
// and skips a file deleted after it was listed.
//
// Why this test is important:
//   - A developer edits the tree while a sync runs; a file gone between the listing
//     and its page must not fail the sync, and paging must neither repeat nor drop
//     the files that remain.
//
// What it tests:
//   - Three files at page size 1, the third deleted after the first page → a.md and
//     b.md, each once, and the crawl ends.
func TestGitSource_PagesSkipAVanishedFile(t *testing.T) {
	t.Parallel()

	tree := gitTree{
		files:     map[string]string{textAMd: textA, gitB: textB, gitC: "c"},
		tracked:   []string{gitC, textAMd, gitB},
		untracked: nil,
		changed:   nil,
	}
	root, src := gitSource(t, &tree, nil, nil)

	page, next, err := src.ListPage(context.Background(), "", 1)
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, textAMd, page[0].Key)
	require.NotEmpty(t, next)

	require.NoError(t, os.Remove(filepath.Join(root, gitC)))

	keys := []string{}

	for next != "" {
		page, next, err = src.ListPage(context.Background(), next, 1)
		require.NoError(t, err)

		for _, obj := range page {
			keys = append(keys, obj.Key)
		}
	}

	assert.Equal(t, []string{gitB}, keys)
}

// TestGitSource_NotAWorkTreeIsInvalidInput tests that a root that is not a git work
// tree is refused with a terminal code.
//
// Why this test is important:
//   - A mis-mounted root must fail the sync as a configuration error, not retry
//     forever or sweep every document as deleted.
//
// What it tests:
//   - IsRepo false → TestConnection and ListPage both return CodeInvalidInput.
func TestGitSource_NotAWorkTreeIsInvalidInput(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	git := mocks.NewMockGitClient(gomock.NewController(t))
	git.EXPECT().IsRepo(gomock.Any(), root).Return(false).Times(gitProbes)

	src, err := connectorgit.NewSource(&connectorgit.Config{
		Root: root, IncludeGlobs: nil, ExcludeGlobs: nil,
	}, git)
	require.NoError(t, err)

	_, err = src.TestConnection(context.Background())
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), err)

	_, _, err = src.ListPage(context.Background(), "", 0)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), err)
}

// TestGitSource_TestConnectionCountsFiles tests that TestConnection returns how many
// files a crawl would list.
//
// Why this test is important:
//   - The probe is what tells an operator the root and globs select the files they
//     meant before the first sync runs.
//
// What it tests:
//   - Two tracked and one untracked file, one excluded by a glob → 2.
func TestGitSource_TestConnectionCountsFiles(t *testing.T) {
	t.Parallel()

	tree := gitTree{
		files:     map[string]string{textAMd: textA, gitB: textB, "gen/c.md": "c"},
		tracked:   []string{textAMd, "gen/c.md"},
		untracked: []string{gitB},
		changed:   nil,
	}
	_, src := gitSource(t, &tree, nil, []string{"gen/**"})

	count, err := src.TestConnection(context.Background())
	require.NoError(t, err)
	assert.Equal(t, gitKept, count)
}

// TestGitSource_EmptyRootIsRefused tests that a source with no root is refused at
// construction.
//
// Why this test is important:
//   - An empty root would read the process's working directory; a consumer that has
//     not configured a root must get a coded refusal instead.
//
// What it tests:
//   - NewSource with Root "" → CodeInvalidInput and no source.
func TestGitSource_EmptyRootIsRefused(t *testing.T) {
	t.Parallel()

	src, err := connectorgit.NewSource(&connectorgit.Config{
		Root: "", IncludeGlobs: nil, ExcludeGlobs: nil,
	}, mocks.NewMockGitClient(gomock.NewController(t)))

	assert.Nil(t, src)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), err)
}

// gitBuilder is the connector SourceBuilder with git as its GitClient.
func gitBuilder(git interfaces.GitClient) interfaces.SourceBuilder {
	return connector.NewSourceBuilder(stdx.Zero[connector.Config](), connector.Deps{
		Decorations:   decorate.Decorations{},
		BuildS3Client: nil,
		STS:           nil,
		GitHub:        nil,
		Git:           &connector.GitDeps{Client: git},
	})
}

// TestSourceFactory_GitKind tests that the connector builder turns a git SourceConfig
// into a working, decorated source over the injected GitClient.
//
// Why this test is important:
//   - The builder is how a consumer reaches the git lister: the kind must map the
//     SourceConfig's root and globs onto the backend, or the sync would list the wrong
//     directory or ingest files the globs exclude.
//
// What it tests:
//   - Build(kind=git, root, include **/*.md) over a Markdown file and a Go file →
//     TestConnection counts the one Markdown file.
func TestSourceFactory_GitKind(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tree := gitTree{
		files:     map[string]string{textAMd: textA, gitMain: "m"},
		tracked:   []string{textAMd, gitMain},
		untracked: nil,
		changed:   nil,
	}

	src, err := gitBuilder(gitMock(t, root, &tree)).Build(context.Background(),
		fixtures.BuildRef(func(row *interfaces.SourceConfig) {
			row.Kind = interfaces.ConnectorKindGit
			row.Root = root
			row.IncludeGlobs = []string{"**/*.md"}
		}),
	)
	require.NoError(t, err)

	count, err := src.TestConnection(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestSourceFactory_GitWithoutRootIsRefused tests that the builder refuses a git
// source with no root or no GitClient.
//
// Why this test is important:
//   - An empty root would list the process's working directory, and a missing
//     GitClient would fail on the first page; both are configuration errors the
//     builder must refuse with a terminal code before any sync starts.
//
// What it tests:
//   - Build(kind=git) with Root "" (GitClient set), and with a root but a nil
//     GitClient → CodeInvalidInput and no source.
func TestSourceFactory_GitWithoutRootIsRefused(t *testing.T) {
	t.Parallel()

	git := mocks.NewMockGitClient(gomock.NewController(t))

	for name, refused := range map[string]struct {
		git  interfaces.GitClient
		root string
	}{
		"no root":       {git: git, root: ""},
		"no git client": {git: nil, root: t.TempDir()},
	} {
		src, err := gitBuilder(refused.git).Build(context.Background(),
			fixtures.BuildRef(func(row *interfaces.SourceConfig) {
				row.Kind = interfaces.ConnectorKindGit
				row.Root = refused.root
			}),
		)

		assert.Nil(t, src, name)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), name, err)
	}
}

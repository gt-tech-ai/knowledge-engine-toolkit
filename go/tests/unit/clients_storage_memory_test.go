package unit_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/memory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// TestStorageMemory_RoundTrip tests that the in-memory StorageClient round-trips an
// object through the object CRUD surface with no external storage.
//
// Why this test is important:
//   - The memory backend is only a useful no-infra stand-in if it behaves like real
//     object storage for the operations services actually use; a partial impl would let a
//     build pass but break the first real call path.
//
// What it tests:
//   - upload → exists/download/stat/list return the stored object; presign (GET + PUT)
//     yields a synthetic memory:// URL; delete + DeleteBatch remove; a missing key
//     is a not-found error.
func TestStorageMemory_RoundTrip(t *testing.T) {
	t.Parallel()

	c := memory.New()
	ctx := context.Background()

	require.NoError(t, c.EnsureBucket(ctx, textB))
	require.NoError(
		t,
		c.Upload(ctx, textB, textK, strings.NewReader("hello"), textTextPlain),
	)

	exists, err := c.Exists(ctx, textB, textK)
	require.NoError(t, err)
	require.True(t, exists)

	rc, err := c.Download(ctx, textB, textK)
	require.NoError(t, err)

	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Equal(t, "hello", string(body))

	obj, err := c.Stat(ctx, textB, textK)
	require.NoError(t, err)
	require.Equal(t, int64(int64Arg5), obj.Size)
	require.Equal(t, textTextPlain, obj.ContentType)

	objs, err := c.ListObjects(ctx, textB, "")
	require.NoError(t, err)
	require.Len(t, objs, 1)
	require.Equal(t, textK, objs[0].Key)

	getURL, err := c.PresignURL(ctx, textB, textK, presignURLArg60, "")

	testStorageMemoryRoundTripRest(ctx, t, c, err, getURL)
}

// testStorageMemoryRoundTripRest continues TestStorageMemory_RoundTrip with its
// remaining steps.
func testStorageMemoryRoundTripRest(
	ctx context.Context, t *testing.T, c *memory.Client, err error, getURL string,
) {
	t.Helper()

	var exists bool

	require.NoError(t, err)
	require.True(
		t,
		strings.HasPrefix(getURL, textMemory),
		"the memory backend returns a synthetic URL",
	)

	putURL, err := c.PresignPutURL(ctx, textB, "k2", presignPutURLArg60, textTextPlain)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(putURL, textMemory))

	require.NoError(
		t,
		c.Upload(ctx, textB, "x1", strings.NewReader(textA), textTextPlain),
	)
	require.NoError(
		t,
		c.Upload(ctx, textB, "x2", strings.NewReader(textB), textTextPlain),
	)
	require.NoError(t, c.DeleteBatch(ctx, textB, []string{"x1", "x2"}))

	remaining, err := c.ListObjects(ctx, textB, textX)
	require.NoError(t, err)
	require.Empty(t, remaining)

	require.NoError(t, c.Delete(ctx, textB, textK))

	exists, err = c.Exists(ctx, textB, textK)
	require.NoError(t, err)
	require.False(t, exists)

	_, err = c.Download(ctx, textB, textMissing)
	require.Error(t, err, "a missing key is a not-found error")

	_, err = c.Stat(ctx, textB, textMissing)
	require.Error(t, err)
}

// TestStorageMemory_ListObjectsPage tests the bounded list: at most `limit` objects, all
// under the prefix — the primitive the connector's TestConnection probe uses instead of
// the unbounded ListObjects (which paginates through an entire bucket).
//
// Why this test is important:
//   - A connector "test connection" probe on a large customer bucket must be bounded;
//     ListObjectsPage is that bound, and it must still filter by prefix like ListObjects.
//
// What it tests:
//   - with 3 objects under docs/ and one outside, ListObjectsPage(docs/, 2) returns
//     exactly 2 (the cap), all under docs/; a limit at/above the match count returns all
//     matches and excludes the non-prefixed key.
func TestStorageMemory_ListObjectsPage(t *testing.T) {
	t.Parallel()

	c := memory.New()
	ctx := context.Background()
	require.NoError(t, c.EnsureBucket(ctx, textB))

	for _, k := range []string{textDocsA, textDocsB, textDocsC, "other/x"} {
		require.NoError(t, c.Upload(ctx, textB, k, strings.NewReader("y"), textTextPlain))
	}

	capped, err := c.ListObjectsPage(ctx, textB, textDocs, limit2)
	require.NoError(t, err)
	require.Len(t, capped, wantCappedLen2, "the limit caps the result")

	for _, o := range capped {
		require.True(
			t,
			strings.HasPrefix(o.Key, textDocs),
			"only prefixed objects are returned",
		)
	}

	all, err := c.ListObjectsPage(ctx, textB, textDocs, limit10)
	require.NoError(t, err)
	require.Len(
		t,
		all,
		wantAllLen3,
		"a limit at/above the match count returns all matches",
	)
}

// TestStorageMemory_ListObjectsPageToken tests the resumable, key-ordered streaming list:
// successive calls threaded by the returned token walk every prefixed object once, in
// order, and terminate with an empty token — the in-memory analogue of S3's
// continuation-token pagination the sync engine crawls.
//
// Why this test is important:
//   - A sync engine pages a bucket with this primitive and checkpoints the token to
//     crash-resume; the memory backend must paginate deterministically (a map has no
//     stable order) so a unit-level engine test can drive the full streaming loop without
//     a real object store.
//
// What it tests:
//   - two paged calls (limit 2 then 2) return docs/a,docs/b then docs/c in key order,
//     exclude the non-prefixed key, and the final page yields an empty resume token.
func TestStorageMemory_ListObjectsPageToken(t *testing.T) {
	t.Parallel()

	c := memory.New()
	ctx := context.Background()
	require.NoError(t, c.EnsureBucket(ctx, textB))

	for _, k := range []string{textDocsC, textDocsA, textDocsB, "other/x"} {
		require.NoError(t, c.Upload(ctx, textB, k, strings.NewReader("y"), textTextPlain))
	}

	first, next, err := c.ListObjectsPageToken(ctx, textB, textDocs, "", limit2)
	require.NoError(t, err)
	require.Len(t, first, wantFirstLen2)
	require.Equal(t, textDocsA, first[0].Key, "key order")
	require.Equal(t, textDocsB, first[1].Key)
	require.Equal(t, textDocsB, next, "resume after the last returned key")

	second, next, err := c.ListObjectsPageToken(ctx, textB, textDocs, next, limit2)
	require.NoError(t, err)
	require.Len(t, second, 1, "only the third prefixed object remains (other/x excluded)")
	require.Equal(t, textDocsC, second[0].Key)
	require.Empty(t, next, "listing exhausted → empty token")
}

// TestStorageMemory_MultipartLifecycle tests the in-memory multipart upload lifecycle so
// a caller's upload path that uses multipart also builds and runs with no S3.
//
// Why this test is important:
//   - A consumer that uses multipart for large uploads needs the memory backend to
//     implement the multipart methods, or its composition root fails to build under
//     kind=memory.
//
// What it tests:
//   - create → list → presign a part → complete yields an (empty) object at the upload's
//     key; a separate create → abort removes the in-progress upload.
func TestStorageMemory_MultipartLifecycle(t *testing.T) {
	t.Parallel()

	c := memory.New()
	ctx := context.Background()

	id, err := c.CreateMultipartUpload(ctx, textB, textBig, "application/octet-stream")
	require.NoError(t, err)
	require.NotEmpty(t, id)

	ups, err := c.ListMultipartUploads(ctx, textB)
	require.NoError(t, err)
	require.Len(t, ups, 1)

	partURL, err := c.PresignUploadPart(
		ctx,
		textB,
		textBig,
		id,
		1,
		presignUploadPartArg60,
	)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(partURL, textMemory))

	require.NoError(
		t,
		c.CompleteMultipartUpload(ctx, textB, textBig, id, []interfaces.CompletedPart{
			{ETag: "e1", PartNumber: 1},
		}),
	)

	exists, err := c.Exists(ctx, textB, textBig)
	require.NoError(t, err)
	require.True(t, exists, "a completed multipart upload yields an object")

	aborted, err := c.CreateMultipartUpload(
		ctx,
		textB,
		"big2",
		"application/octet-stream",
	)
	require.NoError(t, err)
	require.NoError(t, c.AbortMultipartUpload(ctx, textB, "big2", aborted))

	ups, err = c.ListMultipartUploads(ctx, textB)
	require.NoError(t, err)
	require.Empty(t, ups, "no in-progress uploads remain after complete + abort")
}

// TestStorageFactory_KindMemory_Selected tests that the storage factory selects the
// in-memory backend for KindMemory — skipping S3 validation entirely — so a service
// builds with no S3 (the config-selects-impl contract;
// ARCHITECTURE.md#stub-first-backends).
//
// Why this test is important:
//   - The memory backend is opted into by the tier Kind; if NewFromConfig ignored the
//     Kind and validated/built S3, an all-stubs build would fail on the empty S3 config
//     (or dial S3).
//
// What it tests:
//   - NewFromConfig(KindMemory, empty S3Config) succeeds where S3 would fail validation,
//     and the returned client round-trips in memory.
func TestStorageFactory_KindMemory_Selected(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// An empty S3Config fails S3 validation; KindMemory must bypass it and build the
	// memory backend.
	c, err := storage.NewFromConfig(
		ctx,
		&storage.Config{Kind: storage.KindMemory},
		storage.Deps{},
	)
	require.NoError(t, err)

	require.NoError(
		t,
		c.Upload(ctx, textB, textK, strings.NewReader(textX), textTextPlain),
	)

	exists, err := c.Exists(ctx, textB, textK)
	require.NoError(t, err)
	require.True(t, exists, "the memory-backed client round-trips with no S3")
}

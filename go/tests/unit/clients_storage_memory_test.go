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

	client := memory.New()
	ctx := context.Background()

	require.NoError(t, client.EnsureBucket(ctx, textB))
	require.NoError(
		t,
		client.Upload(ctx, textB, textK, strings.NewReader("hello"), textTextPlain),
	)

	exists, err := client.Exists(ctx, textB, textK)
	require.NoError(t, err)
	require.True(t, exists)

	rc, err := client.Download(ctx, textB, textK)
	require.NoError(t, err)

	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Equal(t, "hello", string(body))

	obj, err := client.Stat(ctx, textB, textK)
	require.NoError(t, err)
	require.Equal(t, int64(wantSizeTrip), obj.Size)
	require.Equal(t, textTextPlain, obj.ContentType)

	objs, err := client.ListObjects(ctx, textB, "")
	require.NoError(t, err)
	require.Len(t, objs, 1)
	require.Equal(t, textK, objs[0].Key)

	getURL, err := client.PresignURL(ctx, textB, textK, presignURLInputTrip, "")

	testStorageMemoryRoundTripPresign(ctx, t, client, err, getURL)
}

// testStorageMemoryRoundTripPresign continues TestStorageMemory_RoundTrip: presign.
func testStorageMemoryRoundTripPresign(
	ctx context.Context, t *testing.T, client *memory.Client, err error, getURL string,
) {
	t.Helper()

	var exists bool

	require.NoError(t, err)
	require.True(
		t,
		strings.HasPrefix(getURL, textMemory),
		"the memory backend returns a synthetic URL",
	)

	putURL, err := client.PresignPutURL(
		ctx,
		textB,
		"k2",
		presignPutURLInputPresign,
		textTextPlain,
	)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(putURL, textMemory))

	require.NoError(
		t,
		client.Upload(ctx, textB, "x1", strings.NewReader(textA), textTextPlain),
	)
	require.NoError(
		t,
		client.Upload(ctx, textB, "x2", strings.NewReader(textB), textTextPlain),
	)
	require.NoError(t, client.DeleteBatch(ctx, textB, []string{"x1", "x2"}))

	remaining, err := client.ListObjects(ctx, textB, textX)
	require.NoError(t, err)
	require.Empty(t, remaining)

	require.NoError(t, client.Delete(ctx, textB, textK))

	exists, err = client.Exists(ctx, textB, textK)
	require.NoError(t, err)
	require.False(t, exists)

	_, err = client.Download(ctx, textB, textMissing)
	require.Error(t, err, "a missing key is a not-found error")

	_, err = client.Stat(ctx, textB, textMissing)
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

	client := memory.New()
	ctx := context.Background()
	require.NoError(t, client.EnsureBucket(ctx, textB))

	for _, k := range []string{textDocsA, textDocsB, textDocsC, "other/x"} {
		require.NoError(
			t,
			client.Upload(ctx, textB, k, strings.NewReader("y"), textTextPlain),
		)
	}

	capped, err := client.ListObjectsPage(ctx, textB, textDocs, listObjectsPageInput)
	require.NoError(t, err)
	require.Len(t, capped, wantCappedLen, "the limit caps the result")

	for _, o := range capped {
		require.True(
			t,
			strings.HasPrefix(o.Key, textDocs),
			"only prefixed objects are returned",
		)
	}

	all, err := client.ListObjectsPage(ctx, textB, textDocs, listObjectsPageInputPage)
	require.NoError(t, err)
	require.Len(
		t,
		all,
		wantAllLen,
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

	client := memory.New()
	ctx := context.Background()
	require.NoError(t, client.EnsureBucket(ctx, textB))

	for _, k := range []string{textDocsC, textDocsA, textDocsB, "other/x"} {
		require.NoError(
			t,
			client.Upload(ctx, textB, k, strings.NewReader("y"), textTextPlain),
		)
	}

	first, next, err := client.ListObjectsPageToken(
		ctx,
		textB,
		textDocs,
		"",
		listObjectsPageTokenInputToken,
	)
	require.NoError(t, err)
	require.Len(t, first, wantFirstLen)
	require.Equal(t, textDocsA, first[0].Key, "key order")
	require.Equal(t, textDocsB, first[1].Key)
	require.Equal(t, textDocsB, next, "resume after the last returned key")

	second, next, err := client.ListObjectsPageToken(
		ctx,
		textB,
		textDocs,
		next,
		listObjectsPageTokenInputToken,
	)
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

	client := memory.New()
	ctx := context.Background()

	identifier, err := client.CreateMultipartUpload(
		ctx,
		textB,
		textBig,
		"application/octet-stream",
	)
	require.NoError(t, err)
	require.NotEmpty(t, identifier)

	ups, err := client.ListMultipartUploads(ctx, textB)
	require.NoError(t, err)
	require.Len(t, ups, 1)

	partURL, err := client.PresignUploadPart(
		ctx,
		textB,
		textBig,
		identifier,
		1,
		presignUploadPartInputLifecycle,
	)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(partURL, textMemory))

	require.NoError(
		t,
		client.CompleteMultipartUpload(
			ctx,
			textB,
			textBig,
			identifier,
			[]interfaces.CompletedPart{
				{ETag: "e1", PartNumber: 1},
			},
		),
	)

	exists, err := client.Exists(ctx, textB, textBig)
	require.NoError(t, err)
	require.True(t, exists, "a completed multipart upload yields an object")

	aborted, err := client.CreateMultipartUpload(
		ctx,
		textB,
		"big2",
		"application/octet-stream",
	)
	require.NoError(t, err)
	require.NoError(t, client.AbortMultipartUpload(ctx, textB, "big2", aborted))

	ups, err = client.ListMultipartUploads(ctx, textB)
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
	storageClient, err := storage.NewFromConfig(
		ctx,
		&storage.Config{Kind: storage.KindMemory},
		storage.Deps{},
	)
	require.NoError(t, err)

	require.NoError(
		t,
		storageClient.Upload(ctx, textB, textK, strings.NewReader(textX), textTextPlain),
	)

	exists, err := storageClient.Exists(ctx, textB, textK)
	require.NoError(t, err)
	require.True(t, exists, "the memory-backed client round-trips with no S3")
}

const (
	// listObjectsPageInput is the list objects page input the tests use.
	listObjectsPageInput = 2

	// listObjectsPageInputPage is the list objects page input page the tests use.
	listObjectsPageInputPage = 10

	// listObjectsPageTokenInputToken is the list objects page token input token the tests
	// use.
	listObjectsPageTokenInputToken = 2

	// presignPutURLInputPresign is the presign put urlinput presign the tests use.
	presignPutURLInputPresign = 60

	// presignURLInputTrip is the presign urlinput trip the tests use.
	presignURLInputTrip = 60

	// presignUploadPartInputLifecycle is the presign upload part input lifecycle the tests
	// use.
	presignUploadPartInputLifecycle = 60

	// wantCappedLen is the expected capped len.
	wantCappedLen = 2

	// wantFirstLen is the expected first len.
	wantFirstLen = 2

	// wantSizeTrip is the expected size trip.
	wantSizeTrip = 5
)

const (
	// textBig is the literal "big".
	textBig = "big"

	// textDocsC is the literal "docs/c".
	textDocsC = "docs/c"

	// textMemory is the literal "memory://".
	textMemory = "memory://"
)

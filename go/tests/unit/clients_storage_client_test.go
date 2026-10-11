package unit_test

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage"
	s3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/s3"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// validS3Config is the default S3 config plus the bucket the consumer must name, so it
// passes Validate.
func validS3Config() infra.S3Config {
	cfg := infra.DefaultS3Config()
	cfg.Bucket = "objects"
	return cfg
}

// TestStorageClient_NewFromConfig_BuildsRealClient covers the real-client build path
// (NewFromConfig → New → the aws-sdk client factory) for both backends. It is offline:
// the aws client + presigner are constructed but never call S3.
//
// Why this test is important:
//   - NewFromConfig is the app-wiring entrypoint; broken validation or factory wiring
//     would fail every service at startup rather than at first use.
//
// What it tests:
//   - A valid MinIO config and a valid S3 config each yield a non-nil StorageClient.
func TestStorageClient_NewFromConfig_BuildsRealClient(t *testing.T) {
	t.Parallel()

	minioCfg := validS3Config() // Kind "minio" + endpoint + static creds
	s3Cfg := validS3Config()
	s3Cfg.Kind = "s3"
	s3Cfg.Endpoint = ""
	s3Cfg.AccessKeyID = ""
	s3Cfg.SecretAccessKey = ""

	for name, cfg := range map[string]infra.S3Config{"minio": minioCfg, "s3": s3Cfg} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, err := storage.NewFromConfig(context.Background(), storage.KindS3, cfg)
			require.NoError(t, err)
			require.NotNil(t, c)
		})
	}
}

// TestStorageClient_NewFromConfig_RejectsInvalidConfig covers the validation error path.
//
// Why this test is important:
//   - An unknown backend or empty bucket must fail loudly at construction, not silently
//     mis-route storage at runtime.
//
// What it tests:
//   - An unknown Kind and an empty Bucket each make NewFromConfig return an error.
func TestStorageClient_NewFromConfig_RejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	badKind := validS3Config()
	badKind.Kind = "gcs"
	_, err := storage.NewFromConfig(context.Background(), storage.KindS3, badKind)
	require.Error(t, err)

	noBucket := infra.DefaultS3Config()
	noBucket.Bucket = ""
	_, err = storage.NewFromConfig(context.Background(), storage.KindS3, noBucket)
	require.Error(t, err)
}

// newStorageClientWithMock builds a StorageClient over the injected mock S3 seam. A
// positive threshold forces the multipart path without a multi-megabyte body; 0 uses
// the default. ctx is the construction context passed to storage.New.
func newStorageClientWithMock(
	ctx context.Context,
	t *testing.T,
	api s3.S3API,
	threshold int64,
) interfaces.StorageClient {
	t.Helper()
	c, err := storage.New(
		ctx,
		storage.Config{API: api, MultipartThreshold: threshold},
	)
	require.NoError(t, err)
	return c
}

// TestStorageClient_NotFoundMapping tests that the two distinct S3 not-found errors
// are mapped consistently across Stat/Exists/Download.
//
// Why this test is important:
//   - HeadObject and GetObject surface absence via DIFFERENT typed errors
//     (*types.NotFound vs *types.NoSuchKey); a single-type check silently misses one
//   - Callers depend on errors.ErrNotFound to distinguish "missing" from "broken"
//
// What it tests:
//   - Stat on a missing key returns an errors.ErrNotFound-classified error
//   - Exists on a missing key returns (false, nil), not an error
//   - Download on a missing key returns an errors.ErrNotFound-classified error
func TestStorageClient_NotFoundMapping(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	api.EXPECT().HeadObject(gomock.Any(), gomock.Any()).
		Return(nil, &s3types.NotFound{}).AnyTimes()
	api.EXPECT().GetObject(gomock.Any(), gomock.Any()).
		Return(nil, &s3types.NoSuchKey{})

	c := newStorageClientWithMock(t.Context(), t, api, 0)

	_, statErr := c.Stat(context.Background(), "b", "missing")
	require.Error(t, statErr)
	assert.True(
		t,
		apperr.Is(statErr, apperr.ErrNotFound),
		"Stat must classify NotFound",
	)

	exists, err := c.Exists(context.Background(), "b", "missing")
	require.NoError(t, err)
	assert.False(t, exists, "Exists must return false for a missing key")

	_, dlErr := c.Download(context.Background(), "b", "missing")
	require.Error(t, dlErr)
	assert.True(
		t,
		apperr.Is(dlErr, apperr.ErrNotFound),
		"Download must classify NoSuchKey",
	)
}

// TestStorageClient_Stat_ReturnsMetadata tests that Stat surfaces object metadata.
//
// Why this test is important:
//   - Content-type detection on download depends on Stat reading HeadObject metadata;
//     a wrong field mapping silently loses the MIME type a reader relies on
//
// What it tests:
//   - ContentType, Size, and Key from HeadObject are mapped onto the StorageObject
func TestStorageClient_Stat_ReturnsMetadata(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	api.EXPECT().HeadObject(gomock.Any(), gomock.Any()).Return(&awss3.HeadObjectOutput{
		ContentType:   aws.String("application/pdf"),
		ContentLength: aws.Int64(1234),
		ETag:          aws.String(`"abc"`),
	}, nil)

	c := newStorageClientWithMock(t.Context(), t, api, 0)

	obj, err := c.Stat(context.Background(), "b", "doc.pdf")
	require.NoError(t, err)
	assert.Equal(t, "application/pdf", obj.ContentType)
	assert.Equal(t, int64(1234), obj.Size)
	assert.Equal(t, "doc.pdf", obj.Key)
}

// TestStorageClient_Upload_SetsContentType tests that a small upload passes its
// content-type and body to PutObject.
//
// Why this test is important:
//   - The stored object's Content-Type is set from the upload; if dropped, browsers
//     and downstream parsers mis-handle the document
//
// What it tests:
//   - PutObject receives the given Content-Type and a non-nil body
func TestStorageClient_Upload_SetsContentType(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	var got *awss3.PutObjectInput
	api.EXPECT().PutObject(gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context,
			in *awss3.PutObjectInput,
			_ ...func(*awss3.Options),
		) (*awss3.PutObjectOutput, error) {
			got = in
			return &awss3.PutObjectOutput{}, nil
		},
	)

	c := newStorageClientWithMock(t.Context(), t, api, 0)

	err := c.Upload(
		context.Background(),
		"b",
		"k",
		strings.NewReader("hello"),
		"text/markdown",
	)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "text/markdown", aws.ToString(got.ContentType))
	assert.NotNil(t, got.Body)
}

// TestStorageClient_Upload_ThresholdSelectsSingleVsMultipart tests that Upload picks
// single-part PutObject for small bodies and multipart for large ones.
//
// Why this test is important:
//   - io.Reader bodies have no known length; the client decides by peeking, without
//     buffering the whole (potentially huge) body — a wrong boundary uses slow
//     multipart for tiny files or fails large uploads
//
// What it tests:
//   - A body at/below the threshold drives a single PutObject (no multipart calls)
//   - A body above the threshold drives CreateMultipartUpload → UploadPart →
//     CompleteMultipartUpload (no PutObject)
func TestStorageClient_Upload_ThresholdSelectsSingleVsMultipart(t *testing.T) {
	t.Parallel()

	t.Run("small body → single part", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		api.EXPECT().PutObject(gomock.Any(), gomock.Any()).
			Return(&awss3.PutObjectOutput{}, nil)
		// No multipart expectations: gomock fails if CreateMultipartUpload is called.

		c := newStorageClientWithMock(t.Context(), t, api, 0)
		require.NoError(
			t,
			c.Upload(
				context.Background(),
				"b",
				"k",
				strings.NewReader("small"),
				"text/plain",
			),
		)
	})

	t.Run("large body → multipart", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		api.EXPECT().CreateMultipartUpload(gomock.Any(), gomock.Any()).
			Return(&awss3.CreateMultipartUploadOutput{UploadId: aws.String("u1")}, nil)
		api.EXPECT().UploadPart(gomock.Any(), gomock.Any()).
			Return(&awss3.UploadPartOutput{ETag: aws.String(`"p"`)}, nil).MinTimes(1)
		api.EXPECT().CompleteMultipartUpload(gomock.Any(), gomock.Any()).
			Return(&awss3.CompleteMultipartUploadOutput{}, nil)
		// No PutObject expectation: gomock fails if the single-part path is taken.

		// A 4-byte threshold forces the multipart path for a 6-byte body.
		c := newStorageClientWithMock(t.Context(), t, api, 4)
		require.NoError(
			t,
			c.Upload(
				context.Background(),
				"b",
				"k",
				strings.NewReader("abcdef"),
				"text/plain",
			),
		)
	})
}

// TestStorageClient_ListObjects_MapsContents tests that list output is mapped to
// StorageObjects.
//
// Why this test is important:
//   - Connector sync diffing depends on key/size/etag being mapped correctly; a
//     wrong field mapping silently corrupts diff results
//
// What it tests:
//   - Each S3 object's Key, Size, and ETag are carried onto the StorageObject
func TestStorageClient_ListObjects_MapsContents(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	// The paginator passes an options mutator, so the call carries a third (variadic)
	// argument the matcher must account for.
	api.EXPECT().ListObjectsV2(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&awss3.ListObjectsV2Output{
			Contents: []s3types.Object{
				{Key: aws.String("a.txt"), Size: aws.Int64(10), ETag: aws.String(`"e1"`)},
				{Key: aws.String("b.txt"), Size: aws.Int64(20), ETag: aws.String(`"e2"`)},
			},
		}, nil)

	c := newStorageClientWithMock(t.Context(), t, api, 0)

	objs, err := c.ListObjects(context.Background(), "b", "")
	require.NoError(t, err)
	require.Len(t, objs, 2)
	assert.Equal(t, "a.txt", objs[0].Key)
	assert.Equal(t, int64(10), objs[0].Size)
	assert.Equal(t, `"e2"`, objs[1].ETag)
}

// TestStorageClient_ListObjectsPageToken_StreamsWithToken tests the resumable,
// single-request streaming list: it passes the caller's continuation token through and
// surfaces the next token only while the listing is truncated.
//
// Why this test is important:
//   - A sync engine crash-resumes by checkpointing this token; if the token weren't
//     passed through (re-listing from the start) or the next token leaked when the
//     listing was exhausted (an infinite loop), a large-bucket sync would loop or
//     re-process objects.
//
// What it tests:
//   - The request carries the caller's continuation token; a truncated response yields
//     the next token; a non-truncated response yields "" (loop terminates).
func TestStorageClient_ListObjectsPageToken_StreamsWithToken(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	api.EXPECT().ListObjectsV2(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context, in *awss3.ListObjectsV2Input, _ ...func(*awss3.Options),
		) (*awss3.ListObjectsV2Output, error) {
			assert.Equal(
				t,
				"tok1",
				aws.ToString(in.ContinuationToken),
				"resumes from the caller token",
			)
			return &awss3.ListObjectsV2Output{
				Contents: []s3types.Object{
					{
						Key:  aws.String("docs/a"),
						Size: aws.Int64(5),
						ETag: aws.String(`"e1"`),
					},
				},
				IsTruncated:           aws.Bool(true),
				NextContinuationToken: aws.String("tok2"),
			}, nil
		})

	c := newStorageClientWithMock(t.Context(), t, api, 0)

	objs, next, err := c.ListObjectsPageToken(
		context.Background(),
		"b",
		"docs/",
		"tok1",
		100,
	)
	require.NoError(t, err)
	require.Len(t, objs, 1)
	assert.Equal(t, "docs/a", objs[0].Key)
	assert.Equal(t, "tok2", next, "truncated → resume token surfaced")

	// A non-truncated page terminates the stream (next == "").
	api.EXPECT().ListObjectsV2(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&awss3.ListObjectsV2Output{
			Contents: []s3types.Object{
				{Key: aws.String("docs/b"), ETag: aws.String(`"e2"`)},
			},
			IsTruncated: aws.Bool(false),
		}, nil)
	_, next, err = c.ListObjectsPageToken(context.Background(), "b", "docs/", "tok2", 100)
	require.NoError(t, err)
	assert.Empty(t, next, "exhausted listing → empty token")
}

// TestStorageClient_DeleteBatch_SurfacesPerObjectErrors tests partial-failure
// detection in batch delete.
//
// Why this test is important:
//   - DeleteObjects returns per-object failures in the response body even when the
//     HTTP call succeeds; swallowing them means orphaned-file cleanup silently leaves
//     objects behind while reporting success
//
// What it tests:
//   - A response with a non-empty Errors list makes DeleteBatch return an error
func TestStorageClient_DeleteBatch_SurfacesPerObjectErrors(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	api.EXPECT().
		DeleteObjects(gomock.Any(), gomock.Any()).
		Return(&awss3.DeleteObjectsOutput{
			Errors: []s3types.Error{{
				Key:     aws.String("x"),
				Code:    aws.String("AccessDenied"),
				Message: aws.String("nope"),
			}},
		}, nil)

	c := newStorageClientWithMock(t.Context(), t, api, 0)

	err := c.DeleteBatch(context.Background(), "b", []string{"x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AccessDenied")
}

// TestStorageClient_DeleteBatch_ChunksOverLimit tests that a >1000-key delete is
// split into ≤1000-key DeleteObjects requests.
//
// Why this test is important:
//   - S3 DeleteObjects rejects a request with more than 1000 keys, so a large
//     orphaned-file cleanup previously failed outright; it must be chunked.
//
// What it tests:
//   - Deleting 2500 keys issues 3 DeleteObjects calls, none exceeding 1000 keys,
//     covering every key exactly once.
func TestStorageClient_DeleteBatch_ChunksOverLimit(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)

	var mu sync.Mutex
	calls := 0
	seen := map[string]int{}
	api.EXPECT().DeleteObjects(gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context,
			in *awss3.DeleteObjectsInput,
			_ ...func(*awss3.Options),
		) (*awss3.DeleteObjectsOutput, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			assert.LessOrEqual(
				t,
				len(in.Delete.Objects),
				1000,
				"each request must be within the S3 limit",
			)
			for _, o := range in.Delete.Objects {
				seen[aws.ToString(o.Key)]++
			}
			return &awss3.DeleteObjectsOutput{}, nil
		}).
		Times(3)

	keys := make([]string, 2500)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}
	c := newStorageClientWithMock(t.Context(), t, api, 0)
	require.NoError(t, c.DeleteBatch(context.Background(), "b", keys))

	assert.Equal(t, 3, calls, "2500 keys → ceil(2500/1000) = 3 requests")
	assert.Len(t, seen, 2500, "every key deleted exactly once")
}

// TestStorageClient_DeleteBatch_BuildsIdentifiers tests that batch delete maps keys
// to identifiers and no-ops on an empty slice.
//
// Why this test is important:
//   - Orphaned-file cleanup deletes many keys at once; a wrong mapping silently
//     deletes nothing or the wrong objects, and a spurious call on an empty batch
//     wastes a round-trip
//
// What it tests:
//   - DeleteObjects receives one ObjectIdentifier per key
//   - An empty key slice is a no-op (gomock fails if the API is called a second time)
func TestStorageClient_DeleteBatch_BuildsIdentifiers(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	var got *awss3.DeleteObjectsInput
	api.EXPECT().DeleteObjects(gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context,
			in *awss3.DeleteObjectsInput,
			_ ...func(*awss3.Options),
		) (*awss3.DeleteObjectsOutput, error) {
			got = in
			return &awss3.DeleteObjectsOutput{}, nil
		},
	) // exactly once: the empty batch below must NOT call the API.

	c := newStorageClientWithMock(t.Context(), t, api, 0)

	require.NoError(t, c.DeleteBatch(context.Background(), "b", []string{"a", "b", "c"}))
	require.NotNil(t, got)
	assert.Len(t, got.Delete.Objects, 3)

	require.NoError(t, c.DeleteBatch(context.Background(), "b", nil))
}

// TestStorageClient_Multipart_AbortsWithLiveContextOnCancel tests that a failed
// multipart upload is still aborted when the operation context is already cancelled.
//
// Why this test is important:
//   - putMultipart promises no partial upload lingers on failure; if the abort reused
//     the cancelled operation context, AbortMultipartUpload would no-op and leave an
//     incomplete multipart upload in the bucket, accruing storage cost until a
//     lifecycle rule reaps it
//
// What it tests:
//   - When a part upload fails under an already-cancelled context, the upload errors
//     and AbortMultipartUpload is still invoked with a live (Err() == nil) context
func TestStorageClient_Multipart_AbortsWithLiveContextOnCancel(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	api.EXPECT().CreateMultipartUpload(gomock.Any(), gomock.Any()).
		Return(&awss3.CreateMultipartUploadOutput{UploadId: aws.String("u1")}, nil)
	// MinTimes(1): parts upload concurrently, so both parts of the 6-byte body may
	// be dispatched before the first failure cancels the group — the abort behavior,
	// not the exact part count, is what this test pins.
	api.EXPECT().UploadPart(gomock.Any(), gomock.Any()).
		Return(nil, apperr.Sentinel("part failed")).MinTimes(1)

	var abortCtxErr error
	aborted := false
	api.EXPECT().AbortMultipartUpload(gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			ctx context.Context,
			_ *awss3.AbortMultipartUploadInput,
			_ ...func(*awss3.Options),
		) (*awss3.AbortMultipartUploadOutput, error) {
			aborted = true
			abortCtxErr = ctx.Err()
			return &awss3.AbortMultipartUploadOutput{}, nil
		},
	)

	// A 4-byte threshold forces the multipart path for a small body.
	c := newStorageClientWithMock(t.Context(), t, api, 4)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // operation context already cancelled before the part fails

	err := c.Upload(ctx, "b", "k", strings.NewReader("abcdef"), "text/plain")
	require.Error(t, err)
	require.True(t, aborted, "a failed multipart upload must be aborted")
	assert.NoError(
		t,
		abortCtxErr,
		"abort must run on a live context even when the op ctx is cancelled",
	)
}

// TestStorageClient_Delete_CallsDeleteObject tests that Delete issues a single-object
// DeleteObject for the given bucket and key.
//
// Why this test is important:
//   - Delete is the single-object cleanup path (e.g. removing a superseded document);
//     a wrong bucket/key mapping silently deletes nothing or the wrong object
//
// What it tests:
//   - Delete calls DeleteObject with the given bucket/key and returns no error
func TestStorageClient_Delete_CallsDeleteObject(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	var got *awss3.DeleteObjectInput
	api.EXPECT().DeleteObject(gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context,
			in *awss3.DeleteObjectInput,
			_ ...func(*awss3.Options),
		) (*awss3.DeleteObjectOutput, error) {
			got = in
			return &awss3.DeleteObjectOutput{}, nil
		},
	)

	c := newStorageClientWithMock(t.Context(), t, api, 0)

	require.NoError(t, c.Delete(context.Background(), "b", "k"))
	require.NotNil(t, got)
	assert.Equal(t, "b", aws.ToString(got.Bucket))
	assert.Equal(t, "k", aws.ToString(got.Key))
}

// TestStorageClient_Download_ReturnsBody tests that Download streams the object body
// back to the caller on success.
//
// Why this test is important:
//   - Download is the object read path; if it drops or mis-wires the body the
//     object's content is silently lost
//
// What it tests:
//   - A successful GetObject returns its body stream unchanged
func TestStorageClient_Download_ReturnsBody(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	api.EXPECT().GetObject(gomock.Any(), gomock.Any()).Return(&awss3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader("file-bytes")),
	}, nil)

	c := newStorageClientWithMock(t.Context(), t, api, 0)

	rc, err := c.Download(context.Background(), "b", "k")
	require.NoError(t, err)
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	assert.Equal(t, "file-bytes", string(body))
	require.NoError(t, rc.Close())
}

// TestStorageClient_NotFound_SmithyAPIErrorCode tests the smithy APIError "404"
// fallback in the not-found classifier.
//
// Why this test is important:
//   - S3-compatible backends (MinIO) can surface absence as a generic smithy APIError
//     (code "NoSuchKey"/"NotFound"/"404") rather than the typed SDK errors; without the
//     fallback, "missing" is misclassified as "broken"
//
// What it tests:
//   - A HeadObject smithy APIError with code "404" makes Exists return (false, nil)
func TestStorageClient_NotFound_SmithyAPIErrorCode(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	api := mocks.NewMockS3API(ctrl)
	api.EXPECT().HeadObject(gomock.Any(), gomock.Any()).
		Return(nil, &smithy.GenericAPIError{Code: "404", Message: "not found"})

	c := newStorageClientWithMock(t.Context(), t, api, 0)

	exists, err := c.Exists(context.Background(), "b", "missing")
	require.NoError(t, err)
	assert.False(t, exists, "a smithy 404 must classify as not-found")
}

// TestStorageClient_Presign_BuildsSignedURLs tests presigned GET/PUT URL generation
// through a real (offline) presign client built by NewFromConfig.
//
// Why this test is important:
//   - Presigned URLs enable direct browser↔storage transfer, bypassing the caller's
//     service;
//     a broken presigner or wrong expiry wiring blocks uploads/downloads. Presigning is
//     a local signing operation, so it is unit-testable without any S3 round-trip.
//
// What it tests:
//   - PresignURL and PresignPutURL each return a non-empty URL carrying the bucket/key
func TestStorageClient_Presign_BuildsSignedURLs(t *testing.T) {
	t.Parallel()

	// DefaultS3Config supplies static MinIO credentials, so the presign client signs
	// offline (no network) — the same path the MinIO integration suite exercises live.
	c, err := storage.NewFromConfig(
		context.Background(),
		storage.KindS3,
		validS3Config(),
	)
	require.NoError(t, err)

	getURL, err := c.PresignURL(
		context.Background(),
		"bucket",
		"obj/key.txt",
		900,
		"report.pdf",
	)
	require.NoError(t, err)
	assert.Contains(t, getURL, "bucket")
	assert.Contains(t, getURL, "key.txt")
	// A display filename must be signed into a response-content-disposition override so
	// S3 serves the object as a download rather than rendering it inline.
	assert.Contains(t, getURL, "response-content-disposition")
	assert.Contains(t, getURL, url.QueryEscape("attachment"))
	assert.Contains(t, getURL, "report.pdf")

	putURL, err := c.PresignPutURL(
		context.Background(),
		"bucket",
		"obj/key.txt",
		900,
		"text/plain",
	)
	require.NoError(t, err)
	assert.Contains(t, putURL, "bucket")
	assert.Contains(t, putURL, "key.txt")
}

// TestStorageClient_Presign_UsesPublicEndpoint tests that when a PublicEndpoint is set,
// every presigned URL (GET, PUT, multipart part) bakes in that browser-reachable host
// instead of the in-cluster Endpoint.
//
// Why this test is important:
//   - Browser-direct uploads receive the presigned URL and connect to its host. In
//     Docker/K8s the in-cluster Endpoint (minio:9000) is unresolvable from the browser,
//     so the signed URL must carry the public host or the upload fails with
//     ERR_NAME_NOT_RESOLVED. Server-side ops still use the in-cluster endpoint.
//
// What it tests:
//   - With Endpoint=minio:9000 and PublicEndpoint=localhost:9000, the GET/PUT/part URLs
//     contain localhost:9000 and never the in-cluster minio:9000 host.
func TestStorageClient_Presign_UsesPublicEndpoint(t *testing.T) {
	t.Parallel()

	cfg := validS3Config()
	cfg.Endpoint = "http://minio:9000"           // in-cluster: server-side ops
	cfg.PublicEndpoint = "http://localhost:9000" // browser-reachable: presign only
	c, err := storage.NewFromConfig(context.Background(), storage.KindS3, cfg)
	require.NoError(t, err)

	getURL, err := c.PresignURL(
		context.Background(),
		"bucket",
		"obj/key.txt",
		900,
		"text/plain",
	)
	require.NoError(t, err)
	assert.Contains(
		t,
		getURL,
		"localhost:9000",
		"GET presign must use the public endpoint",
	)
	assert.NotContains(
		t,
		getURL,
		"minio:9000",
		"GET presign must not bake in the in-cluster host",
	)

	putURL, err := c.PresignPutURL(
		context.Background(),
		"bucket",
		"obj/key.txt",
		900,
		"text/plain",
	)
	require.NoError(t, err)
	assert.Contains(
		t,
		putURL,
		"localhost:9000",
		"PUT presign must use the public endpoint",
	)
	assert.NotContains(t, putURL, "minio:9000")

	uploadID := "upload-123"
	partURL, err := c.PresignUploadPart(
		context.Background(),
		"bucket",
		"obj/key.txt",
		uploadID,
		1,
		900,
	)
	require.NoError(t, err)
	assert.Contains(
		t,
		partURL,
		"localhost:9000",
		"multipart part presign must use the public endpoint",
	)
	assert.NotContains(t, partURL, "minio:9000")
}

// TestStorageClient_Presign_EmptyPublicEndpointFallsBack tests that an empty
// PublicEndpoint leaves the presigned URL on the client's own Endpoint (the AWS-S3 /
// bare-metal default, where the endpoint is already browser-reachable).
//
// Why this test is important:
//   - The public-endpoint split must be opt-in: unset ⇒ no behavior change, so AWS S3
//     (public regional endpoint) and bare-metal MinIO (localhost) keep working untouched.
//
// What it tests:
//   - With Endpoint=minio:9000 and PublicEndpoint empty, a presigned PUT keeps
//     minio:9000.
func TestStorageClient_Presign_EmptyPublicEndpointFallsBack(t *testing.T) {
	t.Parallel()

	cfg := validS3Config()
	cfg.Endpoint = "http://minio:9000"
	cfg.PublicEndpoint = "" // opt-in: empty falls back to Endpoint
	c, err := storage.NewFromConfig(context.Background(), storage.KindS3, cfg)
	require.NoError(t, err)

	putURL, err := c.PresignPutURL(
		context.Background(),
		"bucket",
		"k.txt",
		900,
		"text/plain",
	)
	require.NoError(t, err)
	assert.Contains(
		t,
		putURL,
		"minio:9000",
		"empty public_endpoint falls back to the client endpoint",
	)
}

// TestStorageClient_PresignPutURL_SignsContentTypeAndHost locks in that the presigned
// PUT signs exactly {content-type, host} — the pinned-Content-Type contract the upload
// flow is built on. It uses the real client (NewFromConfig) because presigning needs the
// concrete presigner; no network is involved (presigning is offline).
//
// Why this test is important:
//   - The signature constrains the headers listed in X-Amz-SignedHeaders, and
//     content-type is deliberately pinned: DocumentService.PresignUpload signs the
//     format's content type and carries that exact value back on the PresignedURL, and
//     the browser client echoes it verbatim on the PUT (PresignedS3Client sets the
//     Content-Type header to the returned value), so the request's Content-Type always
//     matches the signed one. This asserts the SDK signs content-type + host and nothing
//     extraneous — e.g. no checksum header a browser PUT could not reproduce, which would
//     403 against real S3 with SignatureDoesNotMatch. MinIO tolerates a mismatch, so only
//     this signing-layer assertion guards the contract.
//
// What it tests:
//   - X-Amz-SignedHeaders is exactly "content-type;host", whether or not the content-type
//     string is empty (the presigner signs the header either way).
func TestStorageClient_PresignPutURL_SignsContentTypeAndHost(t *testing.T) {
	t.Parallel()

	cfg := validS3Config()
	cfg.Bucket = "b"
	c, err := storage.NewFromConfig(context.Background(), storage.KindS3, cfg)
	require.NoError(t, err)

	for _, ct := range []string{"application/pdf", ""} {
		presigned, err := c.PresignPutURL(context.Background(), "b", "k.pdf", 300, ct)
		require.NoError(t, err)
		u, err := url.Parse(presigned)
		require.NoError(t, err)
		assert.Equal(
			t,
			"content-type;host",
			u.Query().Get("X-Amz-SignedHeaders"),
			"presigned PUT must sign content-type + host so the client's echoed "+
				"Content-Type (%q) matches the signature",
			ct,
		)
	}
}

// TestStorageClient_EnsureBucket tests the idempotent create-if-absent behavior of
// EnsureBucket over the injected S3 seam.
//
// Why this test is important:
//   - A provisioning tool calls EnsureBucket so a bucket exists before anything
//     writes to it; it
//     must not re-create an existing bucket, must create a missing one, and must
//     treat the already-owned/exists race as success — otherwise seeding is flaky
//     or errors, and uploads fail with NoSuchBucket.
//
// What it tests:
//   - HeadBucket success → no CreateBucket call; HeadBucket failure → CreateBucket;
//     an already-owned CreateBucket error is swallowed; any other error propagates.
func TestStorageClient_EnsureBucket(t *testing.T) {
	t.Parallel()

	const bucket = "documents"
	newClient := func(t *testing.T, api s3.S3API) interfaces.StorageClient {
		t.Helper()
		c, err := storage.New(context.Background(), storage.Config{API: api})
		require.NoError(t, err)
		return c
	}

	t.Run("exists: does not create", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		api.EXPECT().HeadBucket(gomock.Any(), gomock.Any()).
			Return(&awss3.HeadBucketOutput{}, nil) // no CreateBucket expectation
		require.NoError(t, newClient(t, api).EnsureBucket(context.Background(), bucket))
	})

	t.Run("absent: creates", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		api.EXPECT().
			HeadBucket(gomock.Any(), gomock.Any()).
			Return(nil, &s3types.NotFound{})
		api.EXPECT().CreateBucket(gomock.Any(), gomock.Any()).
			Return(&awss3.CreateBucketOutput{}, nil)
		require.NoError(t, newClient(t, api).EnsureBucket(context.Background(), bucket))
	})

	t.Run("absent: already owned is success", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		api.EXPECT().
			HeadBucket(gomock.Any(), gomock.Any()).
			Return(nil, &s3types.NotFound{})
		api.EXPECT().CreateBucket(gomock.Any(), gomock.Any()).
			Return(nil, &s3types.BucketAlreadyOwnedByYou{})
		require.NoError(t, newClient(t, api).EnsureBucket(context.Background(), bucket))
	})

	t.Run("absent: other create error propagates", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		api.EXPECT().
			HeadBucket(gomock.Any(), gomock.Any()).
			Return(nil, &s3types.NotFound{})
		api.EXPECT().CreateBucket(gomock.Any(), gomock.Any()).
			Return(nil, &smithy.GenericAPIError{Code: "AccessDenied"})
		require.Error(t, newClient(t, api).EnsureBucket(context.Background(), bucket))
	})
}

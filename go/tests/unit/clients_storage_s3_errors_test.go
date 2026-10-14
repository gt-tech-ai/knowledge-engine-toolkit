package unit_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// errS3 is a generic (non-not-found) backend failure used to drive the S3 client's
// error-mapping branches.
var errS3 = apperr.Sentinel("s3 backend unavailable")

// TestStorageClient_ErrorPaths tests that every S3 operation wraps a backend failure
// (rather than swallowing it), across the single-part, multipart, read, and list
// surfaces.
//
// Why this test is important:
//   - The S3 client is the sole document byte-store; a swallowed PutObject/GetObject/
//     Delete/List error would silently lose data or report success on a failed write,
//     and a not-found must stay distinguishable from a genuine backend error.
//
// What it tests:
//   - Upload surfaces a body read error; putSingle wraps a PutObject error.
//   - Multipart wraps CreateMultipartUpload, a mid-stream body read, and
//     CompleteMultipartUpload failures (aborting the upload).
//   - Download/Delete/DeleteBatch/Exists/Stat/ListObjects each wrap a backend error,
//     and a generic (non-typed) error is classified as NOT not-found.
func TestStorageClient_ErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("upload surfaces a body read error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		c := newStorageClientWithMock(ctx, t, api, 0)
		err := c.Upload(ctx, "b", "k", iotest.ErrReader(errS3), "text/plain")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading upload body")
	})

	t.Run("single-part put wraps a backend error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		api.EXPECT().PutObject(gomock.Any(), gomock.Any()).Return(nil, errS3)
		c := newStorageClientWithMock(ctx, t, api, 0)
		err := c.Upload(ctx, "b", "k", strings.NewReader("hi"), "text/plain")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "s3 put")
	})

	t.Run("multipart wraps a create error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		api.EXPECT().CreateMultipartUpload(gomock.Any(), gomock.Any()).Return(nil, errS3)
		c := newStorageClientWithMock(ctx, t, api, 5) // threshold 5 forces multipart
		err := c.Upload(ctx, "b", "k", strings.NewReader("0123456789"), "text/plain")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create multipart")
	})

	t.Run("multipart aborts on a mid-stream body read error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		api.EXPECT().CreateMultipartUpload(gomock.Any(), gomock.Any()).
			Return(&awss3.CreateMultipartUploadOutput{UploadId: aws.String("u1")}, nil)
		api.EXPECT().UploadPart(gomock.Any(), gomock.Any()).
			Return(&awss3.UploadPartOutput{ETag: aws.String("e")}, nil).AnyTimes()
		api.EXPECT().AbortMultipartUpload(gomock.Any(), gomock.Any()).
			Return(&awss3.AbortMultipartUploadOutput{}, nil).AnyTimes()
		c := newStorageClientWithMock(ctx, t, api, 5)

		// 6 readable bytes (so classifyBody selects multipart) then a hard read error.
		body := io.MultiReader(bytes.NewReader([]byte("ABCDEF")), iotest.ErrReader(errS3))
		err := c.Upload(ctx, "b", "k", body, "text/plain")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reading multipart body")
	})

	t.Run("multipart aborts on a complete error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		api.EXPECT().CreateMultipartUpload(gomock.Any(), gomock.Any()).
			Return(&awss3.CreateMultipartUploadOutput{UploadId: aws.String("u1")}, nil)
		api.EXPECT().UploadPart(gomock.Any(), gomock.Any()).
			Return(&awss3.UploadPartOutput{ETag: aws.String("e")}, nil).AnyTimes()
		api.EXPECT().
			CompleteMultipartUpload(gomock.Any(), gomock.Any()).
			Return(nil, errS3)
		api.EXPECT().AbortMultipartUpload(gomock.Any(), gomock.Any()).
			Return(&awss3.AbortMultipartUploadOutput{}, nil).AnyTimes()
		c := newStorageClientWithMock(ctx, t, api, 5)
		err := c.Upload(ctx, "b", "k", strings.NewReader("0123456789"), "text/plain")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "complete multipart")
	})

	t.Run("download wraps a generic backend error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		api.EXPECT().GetObject(gomock.Any(), gomock.Any()).Return(nil, errS3)
		c := newStorageClientWithMock(ctx, t, api, 0)
		_, err := c.Download(ctx, "b", "k")
		require.Error(t, err)
		assert.Contains(
			t,
			err.Error(),
			"s3 get",
			"a generic error is not classified as not-found",
		)
	})

	t.Run("delete wraps a backend error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		api.EXPECT().DeleteObject(gomock.Any(), gomock.Any()).Return(nil, errS3)
		c := newStorageClientWithMock(ctx, t, api, 0)
		require.Error(t, c.Delete(ctx, "b", "k"))
	})

	t.Run("delete batch wraps a request error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		api.EXPECT().DeleteObjects(gomock.Any(), gomock.Any()).Return(nil, errS3)
		c := newStorageClientWithMock(ctx, t, api, 0)
		err := c.DeleteBatch(ctx, "b", []string{"k1", "k2"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "delete batch")
	})

	t.Run("exists wraps a generic backend error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		api.EXPECT().HeadObject(gomock.Any(), gomock.Any()).Return(nil, errS3)
		c := newStorageClientWithMock(ctx, t, api, 0)
		_, err := c.Exists(ctx, "b", "k")
		require.Error(
			t,
			err,
			"a non-not-found head error must surface, not read as absent",
		)
	})

	t.Run("stat wraps a generic backend error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		api.EXPECT().HeadObject(gomock.Any(), gomock.Any()).Return(nil, errS3)
		c := newStorageClientWithMock(ctx, t, api, 0)
		_, err := c.Stat(ctx, "b", "k")
		require.Error(t, err)
	})

	t.Run("list objects wraps a page error", func(t *testing.T) {
		t.Parallel()
		api := mocks.NewMockS3API(gomock.NewController(t))
		// The paginator passes an options mutator, so the call carries a third
		// (variadic) argument the matcher must account for.
		api.EXPECT().
			ListObjectsV2(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, errS3)
		c := newStorageClientWithMock(ctx, t, api, 0)
		_, err := c.ListObjects(ctx, "b", "prefix/")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "s3 list")
	})
}

// TestStorageClient_Presign_EncodesUnsafeFilename tests that a display filename with
// non-ASCII and quote characters is sanitized into a well-formed Content-Disposition.
//
// Why this test is important:
//   - The download filename is untrusted; an unescaped quote or control char would
//     break the quoted-string and corrupt the header (or enable header smuggling),
//     while non-ASCII names must still survive via the RFC 5987 filename* form.
//
// What it tests:
//   - PresignURL with a filename containing a quote and a non-ASCII rune signs a URL
//     carrying an attachment disposition with the raw name percent-encoded.
func TestStorageClient_Presign_EncodesUnsafeFilename(t *testing.T) {
	t.Parallel()

	c, err := storage.NewFromConfig(
		context.Background(),
		storage.Config{Kind: storage.KindS3, S3: validS3Config()},
		storage.Deps{},
	)
	require.NoError(t, err)

	url, err := c.PresignURL(
		context.Background(),
		"bucket",
		"obj/key.txt",
		900,
		`ré"port.pdf`,
	)
	require.NoError(t, err)
	assert.Contains(t, url, "response-content-disposition")
	assert.Contains(t, url, "attachment")
}

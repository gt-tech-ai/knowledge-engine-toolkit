package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestStorageClient_MultipartSurface tests the presigned-multipart control-plane
// methods (create/complete/abort/list) that the browser-direct large-file upload
// flow and the abandoned-upload cleanup sweep depend on. PresignUploadPart needs the
// concrete presign client (nil on the mock seam, like PresignURL/PresignPutURL), so
// it is exercised live against MinIO in the integration suite, not here.
//
// Why this test is important:
//   - CompleteMultipartUpload assembles the object from client-reported parts; a
//     wrong part-number/ETag mapping corrupts the object or fails completion.
//   - The cleanup sweep depends on ListMultipartUploads mapping the S3 output to abort
//     abandoned uploads, which are invisible to ListObjects until completed.
//
// What it tests:
//   - CreateMultipartUpload returns the S3 upload id and pins key + content-type.
//   - CompleteMultipartUpload forwards parts with matching, ordered part numbers + ETags.
//   - AbortMultipartUpload targets the given upload id.
//   - ListMultipartUploads maps key/upload-id/initiated from the S3 output.
//   - Each control-plane method propagates an S3 API failure (wrapped, errors.Is-able)
//     rather than swallowing it and reporting a false success.
func TestStorageClient_MultipartSurface(t *testing.T) {
	t.Parallel()

	t.Run("create returns upload id", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		var got *awss3.CreateMultipartUploadInput
		api.EXPECT().CreateMultipartUpload(gomock.Any(), gomock.Any()).DoAndReturn(
			func(
				_ context.Context,
				in *awss3.CreateMultipartUploadInput,
				_ ...func(*awss3.Options),
			) (*awss3.CreateMultipartUploadOutput, error) {
				got = in
				return &awss3.CreateMultipartUploadOutput{
					UploadId: aws.String("upload-123"),
				}, nil
			},
		)

		c := newStorageClientWithMock(t.Context(), t, api, 0)
		id, err := c.CreateMultipartUpload(
			context.Background(),
			"b",
			"tenant/doc/file.pdf",
			"application/pdf",
		)
		require.NoError(t, err)
		assert.Equal(t, "upload-123", id)
		require.NotNil(t, got)
		assert.Equal(t, "application/pdf", aws.ToString(got.ContentType))
		assert.Equal(t, "tenant/doc/file.pdf", aws.ToString(got.Key))
	})

	t.Run("complete forwards ordered parts", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		var got *awss3.CompleteMultipartUploadInput
		api.EXPECT().CompleteMultipartUpload(gomock.Any(), gomock.Any()).DoAndReturn(
			func(
				_ context.Context,
				in *awss3.CompleteMultipartUploadInput,
				_ ...func(*awss3.Options),
			) (*awss3.CompleteMultipartUploadOutput, error) {
				got = in
				return &awss3.CompleteMultipartUploadOutput{}, nil
			},
		)

		c := newStorageClientWithMock(t.Context(), t, api, 0)
		err := c.CompleteMultipartUpload(
			context.Background(),
			"b",
			"k",
			"upload-123",
			[]interfaces.CompletedPart{
				{PartNumber: 1, ETag: `"etag-1"`},
				{PartNumber: 2, ETag: `"etag-2"`},
			},
		)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "upload-123", aws.ToString(got.UploadId))
		require.Len(t, got.MultipartUpload.Parts, 2)
		assert.Equal(t, int32(1), aws.ToInt32(got.MultipartUpload.Parts[0].PartNumber))
		assert.Equal(t, `"etag-1"`, aws.ToString(got.MultipartUpload.Parts[0].ETag))
		assert.Equal(t, int32(2), aws.ToInt32(got.MultipartUpload.Parts[1].PartNumber))
		assert.Equal(t, `"etag-2"`, aws.ToString(got.MultipartUpload.Parts[1].ETag))
	})

	t.Run("complete sorts out-of-order parts", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		var got *awss3.CompleteMultipartUploadInput
		api.EXPECT().CompleteMultipartUpload(gomock.Any(), gomock.Any()).DoAndReturn(
			func(
				_ context.Context,
				in *awss3.CompleteMultipartUploadInput,
				_ ...func(*awss3.Options),
			) (*awss3.CompleteMultipartUploadOutput, error) {
				got = in
				return &awss3.CompleteMultipartUploadOutput{}, nil
			},
		)

		c := newStorageClientWithMock(t.Context(), t, api, 0)
		// Parts reported out of order (parallel uploads finish in any order); S3 rejects
		// an unordered list, so the client must sort them ascending before completing.
		err := c.CompleteMultipartUpload(
			context.Background(), "b", "k", "u",
			[]interfaces.CompletedPart{
				{PartNumber: 3, ETag: `"e3"`},
				{PartNumber: 1, ETag: `"e1"`},
				{PartNumber: 2, ETag: `"e2"`},
			},
		)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Len(t, got.MultipartUpload.Parts, 3)
		assert.Equal(t, int32(1), aws.ToInt32(got.MultipartUpload.Parts[0].PartNumber))
		assert.Equal(t, `"e1"`, aws.ToString(got.MultipartUpload.Parts[0].ETag))
		assert.Equal(t, int32(2), aws.ToInt32(got.MultipartUpload.Parts[1].PartNumber))
		assert.Equal(t, int32(3), aws.ToInt32(got.MultipartUpload.Parts[2].PartNumber))
		assert.Equal(t, `"e3"`, aws.ToString(got.MultipartUpload.Parts[2].ETag))
	})

	t.Run("abort targets upload id", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		var got *awss3.AbortMultipartUploadInput
		api.EXPECT().AbortMultipartUpload(gomock.Any(), gomock.Any()).DoAndReturn(
			func(
				_ context.Context,
				in *awss3.AbortMultipartUploadInput,
				_ ...func(*awss3.Options),
			) (*awss3.AbortMultipartUploadOutput, error) {
				got = in
				return &awss3.AbortMultipartUploadOutput{}, nil
			},
		)

		c := newStorageClientWithMock(t.Context(), t, api, 0)
		require.NoError(
			t,
			c.AbortMultipartUpload(context.Background(), "b", "k", "upload-xyz"),
		)
		require.NotNil(t, got)
		assert.Equal(t, "upload-xyz", aws.ToString(got.UploadId))
	})

	t.Run("list maps in-progress uploads", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		api := mocks.NewMockS3API(ctrl)
		initiated := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
		api.EXPECT().ListMultipartUploads(gomock.Any(), gomock.Any()).Return(
			&awss3.ListMultipartUploadsOutput{
				Uploads: []s3types.MultipartUpload{{
					Key:       aws.String("tenant/doc/file.pdf"),
					UploadId:  aws.String("u-1"),
					Initiated: aws.Time(initiated),
				}},
			}, nil,
		)

		c := newStorageClientWithMock(t.Context(), t, api, 0)
		ups, err := c.ListMultipartUploads(context.Background(), "b")
		require.NoError(t, err)
		require.Len(t, ups, 1)
		assert.Equal(t, "tenant/doc/file.pdf", ups[0].Key)
		assert.Equal(t, "u-1", ups[0].UploadID)
		assert.Equal(t, initiated.Format(time.RFC3339), ups[0].Initiated)
	})

	// An S3 control-plane failure must surface to the caller (wrapped, errors.Is-able),
	// not be swallowed into a false success: the multipart initiate/complete/abort/cleanup
	// flows all branch on these errors to compensate (abort the upload, reap the doc).
	t.Run("surfaces api errors", func(t *testing.T) {
		t.Parallel()
		apiErr := apperr.Sentinel("s3 unavailable")

		t.Run("create", func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			api := mocks.NewMockS3API(ctrl)
			api.EXPECT().
				CreateMultipartUpload(gomock.Any(), gomock.Any()).
				Return(nil, apiErr)
			c := newStorageClientWithMock(t.Context(), t, api, 0)
			_, err := c.CreateMultipartUpload(
				context.Background(),
				"b",
				"k",
				"text/plain",
			)
			require.ErrorIs(t, err, apiErr)
		})

		t.Run("complete", func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			api := mocks.NewMockS3API(ctrl)
			api.EXPECT().
				CompleteMultipartUpload(gomock.Any(), gomock.Any()).
				Return(nil, apiErr)
			c := newStorageClientWithMock(t.Context(), t, api, 0)
			err := c.CompleteMultipartUpload(
				context.Background(), "b", "k", "u",
				[]interfaces.CompletedPart{{PartNumber: 1, ETag: `"e1"`}},
			)
			require.ErrorIs(t, err, apiErr)
		})

		t.Run("abort", func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			api := mocks.NewMockS3API(ctrl)
			api.EXPECT().
				AbortMultipartUpload(gomock.Any(), gomock.Any()).
				Return(nil, apiErr)
			c := newStorageClientWithMock(t.Context(), t, api, 0)
			require.ErrorIs(
				t,
				c.AbortMultipartUpload(context.Background(), "b", "k", "u"),
				apiErr,
			)
		})

		t.Run("list", func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			api := mocks.NewMockS3API(ctrl)
			api.EXPECT().
				ListMultipartUploads(gomock.Any(), gomock.Any()).
				Return(nil, apiErr)
			c := newStorageClientWithMock(t.Context(), t, api, 0)
			_, err := c.ListMultipartUploads(context.Background(), "b")
			require.ErrorIs(t, err, apiErr)
		})
	})
}

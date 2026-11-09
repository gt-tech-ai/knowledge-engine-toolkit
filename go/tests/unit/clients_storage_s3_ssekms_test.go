package unit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// newSSEKMSStorage builds the s3 storage client over api with keyID as its SSE-KMS key.
func newSSEKMSStorage(
	t *testing.T,
	api *mocks.MockS3API,
	keyID string,
	threshold int64,
) interfaces.StorageClient {
	t.Helper()

	storageClient, err := storage.NewFromConfig(t.Context(), &storage.Config{
		Kind:               storage.KindS3,
		MultipartThreshold: threshold,
		S3:                 infra.S3Config{SSEKMSKeyID: keyID},
	}, storage.Deps{API: api})
	require.NoError(t, err)

	return storageClient
}

// TestS3_SSEKMSOnUpload tests that a configured KMS key encrypts every write.
//
// Why this test is important:
//   - A bucket whose policy requires SSE-KMS (a deploy plan store) rejects an
//     unencrypted PutObject; an unset key must leave the request unchanged for every
//     other bucket.
//
// What it tests:
//   - With a key, PutObject and CreateMultipartUpload carry aws:kms and the key id;
//     without one, neither field is set.
func TestS3_SSEKMSOnUpload(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	api := mocks.NewMockS3API(gomock.NewController(t))
	api.EXPECT().PutObject(gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options),
		) (*awss3.PutObjectOutput, error) {
			assert.Equal(t, s3types.ServerSideEncryptionAwsKms, in.ServerSideEncryption)
			assert.Equal(t, "arn:key", aws.ToString(in.SSEKMSKeyId))

			return &awss3.PutObjectOutput{}, nil
		})
	require.NoError(t, newSSEKMSStorage(t, api, "arn:key", 0).
		Upload(ctx, textB, textK, strings.NewReader("hi"), textTextPlain))

	multi := mocks.NewMockS3API(gomock.NewController(t))
	multi.EXPECT().CreateMultipartUpload(gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context,
			in *awss3.CreateMultipartUploadInput,
			_ ...func(*awss3.Options),
		) (*awss3.CreateMultipartUploadOutput, error) {
			assert.Equal(t, s3types.ServerSideEncryptionAwsKms, in.ServerSideEncryption)
			assert.Equal(t, "arn:key", aws.ToString(in.SSEKMSKeyId))

			return nil, errS3
		})
	require.Error(t, newSSEKMSStorage(t, multi, "arn:key", 5).
		Upload(ctx, textB, textK, strings.NewReader("0123456789"), textTextPlain))

	plain := mocks.NewMockS3API(gomock.NewController(t))
	plain.EXPECT().PutObject(gomock.Any(), gomock.Any()).DoAndReturn(
		func(
			_ context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options),
		) (*awss3.PutObjectOutput, error) {
			assert.Empty(t, in.ServerSideEncryption)
			assert.Nil(t, in.SSEKMSKeyId)

			return &awss3.PutObjectOutput{}, nil
		})
	require.NoError(t, newSSEKMSStorage(t, plain, "", 0).
		Upload(ctx, textB, textK, strings.NewReader("hi"), textTextPlain))
}

// TestS3_DownloadMissingKeyIsNotFound tests the missing-object classification.
//
// Why this test is important:
//   - A plan store reads "no saved plan yet" as NOT_FOUND; a missing key reported as
//     INTERNAL would fail every first deploy.
//
// What it tests:
//   - GetObject returning NoSuchKey makes Download CodeNotFound.
func TestS3_DownloadMissingKeyIsNotFound(t *testing.T) {
	t.Parallel()

	api := mocks.NewMockS3API(gomock.NewController(t))
	api.EXPECT().GetObject(gomock.Any(), gomock.Any()).Return(nil, &s3types.NoSuchKey{})

	_, err := newSSEKMSStorage(t, api, "", 0).Download(t.Context(), textB, textMissing)
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
}

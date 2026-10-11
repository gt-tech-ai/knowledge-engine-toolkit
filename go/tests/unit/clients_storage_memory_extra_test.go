package unit_test

import (
	"context"
	"testing"
	"testing/iotest"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/memory"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestStorageMemory_ErrorPaths tests the in-memory storage client's guard branches:
// a body read failure on Upload, completing an unknown multipart upload, and
// completing an upload under a bucket/key other than the one it was created for.
//
// Why this test is important:
//   - The memory backend stands in for S3 in dev/test; if it swallowed a body read
//     error, completed a non-existent upload, or completed an upload at whatever
//     location the caller named, tests would pass against behavior the real S3
//     client rejects (NoSuchUpload), hiding upload/multipart wiring bugs.
//
// What it tests:
//   - Upload surfaces a reader error; CompleteMultipartUpload on an unknown upload id
//     returns a not-found error.
//   - CompleteMultipartUpload with a key that does not match the upload returns
//     CodeNotFound and writes no object at that key.
func TestStorageMemory_ErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := memory.New()

	err := c.Upload(ctx, "b", "k", iotest.ErrReader(errS3), "text/plain")
	require.Error(t, err, "a body read failure must surface, not be stored as empty")

	err = c.CompleteMultipartUpload(ctx, "b", "k", "no-such-upload", nil)
	require.Error(t, err, "completing an unknown upload id must be a not-found error")

	id, err := c.CreateMultipartUpload(ctx, "b", "intended", "application/octet-stream")
	require.NoError(t, err)
	err = c.CompleteMultipartUpload(ctx, "b", "elsewhere", id, nil)
	require.Equal(
		t,
		apperr.CodeNotFound,
		apperr.Code(err),
		"a mismatched key is not the upload: %v",
		err,
	)
	exists, err := c.Exists(ctx, "b", "elsewhere")
	require.NoError(t, err)
	require.False(t, exists, "a mismatched complete must not write an object")
}

// TestStorageClient_Exists_ReturnsTrueWhenPresent tests that Exists reports true when
// HeadObject succeeds.
//
// Why this test is important:
//   - Exists gates orphaned-file cleanup and confirm-upload flows; a present object
//     misreported as absent would delete or re-request live data.
//
// What it tests:
//   - A successful HeadObject makes Exists return (true, nil).
func TestStorageClient_Exists_ReturnsTrueWhenPresent(t *testing.T) {
	t.Parallel()

	api := mocks.NewMockS3API(gomock.NewController(t))
	api.EXPECT().HeadObject(gomock.Any(), gomock.Any()).
		Return(&awss3.HeadObjectOutput{ContentLength: aws.Int64(5)}, nil)
	c := newStorageClientWithMock(t.Context(), t, api, 0)

	exists, err := c.Exists(context.Background(), "b", "k")
	require.NoError(t, err)
	assert.True(t, exists, "a present object must report exists=true")
}

package s3

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/sync/errgroup"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// sortParts orders completed parts by ascending part number, which S3 requires;
// concurrent uploads finish in arbitrary order.
func sortParts(parts []types.CompletedPart) {
	slices.SortFunc(parts, func(a, b types.CompletedPart) int {
		return cmp.Compare(aws.ToInt32(a.PartNumber), aws.ToInt32(b.PartNumber))
	})
}

// CreateMultipartUpload begins a multipart upload and returns its S3 upload id for
// the browser-direct part-upload flow.
func (c *s3Client) CreateMultipartUpload(
	ctx context.Context,
	bucket, key, contentType string,
) (string, error) {
	var createMultipartUploadInput s3.CreateMultipartUploadInput

	createMultipartUploadInput.Bucket = aws.String(bucket)
	createMultipartUploadInput.Key = aws.String(key)
	createMultipartUploadInput.ContentType = aws.String(contentType)

	out, err := c.api.CreateMultipartUpload(ctx, &createMultipartUploadInput)
	if err != nil {
		return "", apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 create multipart %s/%s", bucket, key),
		)
	}

	return aws.ToString(out.UploadId), nil
}

// PresignUploadPart returns a presigned PUT URL for one part of a multipart upload,
// valid for expirySeconds.
func (c *s3Client) PresignUploadPart(
	ctx context.Context,
	bucket, key, uploadID string,
	partNumber int32,
	expirySeconds int,
) (string, error) {
	var uploadPartInput s3.UploadPartInput

	uploadPartInput.Bucket = aws.String(bucket)
	uploadPartInput.Key = aws.String(key)
	uploadPartInput.UploadId = aws.String(uploadID)
	uploadPartInput.PartNumber = aws.Int32(partNumber)

	req, err := c.presign.PresignUploadPart(
		ctx,
		&uploadPartInput,
		c.presignOpts(expirySeconds),
	)
	if err != nil {
		return "", apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("presign upload part %d %s/%s", partNumber, bucket, key),
		)
	}

	return req.URL, nil
}

// CompleteMultipartUpload finalizes a multipart upload from the client-reported
// parts (part number + ETag), assembling them into the object at key.
func (c *s3Client) CompleteMultipartUpload(
	ctx context.Context,
	bucket, key, uploadID string,
	parts []interfaces.CompletedPart,
) error {
	completed := make([]types.CompletedPart, len(parts))
	for index, p := range parts {
		var completedPart types.CompletedPart

		completedPart.ETag = aws.String(p.ETag)
		completedPart.PartNumber = aws.Int32(p.PartNumber)

		completed[index] = completedPart
	}

	// S3 requires the completed parts in ascending part-number order and rejects an
	// unordered list with InvalidPartOrder. The parts come from untrusted client input
	// (a browser may report parallel-uploaded parts in completion order), so sort
	// defensively at the S3 boundary rather than trusting the caller.
	slices.SortFunc(completed, func(a, b types.CompletedPart) int {
		return cmp.Compare(aws.ToInt32(a.PartNumber), aws.ToInt32(b.PartNumber))
	})

	var completeMultipartUploadInput s3.CompleteMultipartUploadInput

	completeMultipartUploadInput.Bucket = aws.String(bucket)
	completeMultipartUploadInput.Key = aws.String(key)
	completeMultipartUploadInput.UploadId = aws.String(uploadID)
	completeMultipartUploadInput.MultipartUpload = &types.CompletedMultipartUpload{
		Parts: completed,
	}

	_, err := c.api.CompleteMultipartUpload(ctx, &completeMultipartUploadInput)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 complete multipart %s/%s", bucket, key),
		)
	}

	return nil
}

// AbortMultipartUpload discards an in-progress multipart upload, releasing its parts.
func (c *s3Client) AbortMultipartUpload(
	ctx context.Context,
	bucket, key, uploadID string,
) error {
	_, err := c.api.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:               aws.String(bucket),
		Key:                  aws.String(key),
		UploadId:             aws.String(uploadID),
		ExpectedBucketOwner:  nil,
		IfMatchInitiatedTime: nil,
		RequestPayer:         "",
	})
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 abort multipart %s/%s", bucket, key),
		)
	}

	return nil
}

// ListMultipartUploads returns the in-progress multipart uploads in a bucket,
// transparently paginating.
func (c *s3Client) ListMultipartUploads(
	ctx context.Context,
	bucket string,
) ([]interfaces.MultipartUpload, error) {
	var listMultipartUploadsInput s3.ListMultipartUploadsInput

	listMultipartUploadsInput.Bucket = aws.String(bucket)

	pager := s3.NewListMultipartUploadsPaginator(c.api, &listMultipartUploadsInput)

	var uploads []interfaces.MultipartUpload

	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInternal,
				"s3 list multipart uploads "+bucket,
			)
		}

		uploads = slices.Grow(uploads, len(page.Uploads))
		for _, u := range page.Uploads {
			uploads = append(uploads, interfaces.MultipartUpload{
				Key:       aws.ToString(u.Key),
				UploadID:  aws.ToString(u.UploadId),
				Initiated: formatTime(u.Initiated),
			})
		}
	}

	return uploads, nil
}

// putMultipart streams a large object as a multipart upload. Parts are read
// sequentially into their own buffers (an io.Reader cannot be seeked) but their
// UploadPart calls run concurrently (bounded by maxUploadConcurrency), so a big
// document's part PUTs overlap instead of blocking one another — the
// dominant win over the former strictly-serial loop. Any part failure aborts the
// whole upload so no partial upload lingers. Part size equals the threshold (S3
// requires every part except the last to be at least 5 MiB).
func (c *s3Client) putMultipart(
	ctx context.Context,
	bucket, key string,
	body io.Reader,
	contentType string,
) error {
	var createMultipartUploadInput s3.CreateMultipartUploadInput

	createMultipartUploadInput.Bucket = aws.String(bucket)
	createMultipartUploadInput.Key = aws.String(key)
	createMultipartUploadInput.ContentType = aws.String(contentType)

	createIn := &createMultipartUploadInput

	createIn.ServerSideEncryption, createIn.SSEKMSKeyId = c.sseKMS()

	created, err := c.api.CreateMultipartUpload(ctx, createIn)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 create multipart %s/%s", bucket, key),
		)
	}

	currentMultipartUpload := &multipartUpload{
		c:        c,
		uploadID: created.UploadId,
		bucket:   bucket,
		key:      key,
		parts:    nil,
		mu:       sync.Mutex{},
	}

	err = currentMultipartUpload.uploadParts(ctx, body)
	if err != nil {
		currentMultipartUpload.abort(ctx)

		return err
	}

	sortParts(currentMultipartUpload.parts)

	return currentMultipartUpload.complete(ctx)
}

// multipartUpload is one in-progress S3 multipart upload.
type multipartUpload struct {
	// c is the client running the upload.
	c *s3Client

	// uploadID identifies the upload to S3.
	uploadID *string

	// bucket is the destination bucket.
	bucket string

	// key is the destination object key.
	key string

	// parts holds the uploaded parts; guarded by mu until the uploads finish.
	parts []types.CompletedPart

	// mu guards parts, which part uploads finish appending out of order.
	mu sync.Mutex
}

// complete finishes the upload from its sorted parts, aborting it when the store refuses.
func (u *multipartUpload) complete(ctx context.Context) error {
	var completeMultipartUploadInput s3.CompleteMultipartUploadInput

	completeMultipartUploadInput.Bucket = aws.String(u.bucket)
	completeMultipartUploadInput.Key = aws.String(u.key)
	completeMultipartUploadInput.UploadId = u.uploadID
	completeMultipartUploadInput.MultipartUpload = &types.CompletedMultipartUpload{
		Parts: u.parts,
	}

	_, err := u.c.api.CompleteMultipartUpload(ctx, &completeMultipartUploadInput)
	if err != nil {
		u.abort(ctx)

		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 complete multipart %s/%s", u.bucket, u.key),
		)
	}

	return nil
}

// abort discards the in-progress upload on any failure so no incomplete multipart upload
// lingers (and accrues storage cost until a lifecycle rule reaps it). It detaches from
// ctx's cancellation: when a part fails *because* the operation ctx was cancelled or
// timed out, reusing that ctx would make AbortMultipartUpload a no-op and leave the
// upload dangling. WithoutCancel preserves trace/values while dropping the deadline.
func (u *multipartUpload) abort(ctx context.Context) {
	_, _ = u.c.api.AbortMultipartUpload(
		context.WithoutCancel(ctx),
		&s3.AbortMultipartUploadInput{
			Bucket: aws.String(u.bucket), Key: aws.String(u.key), UploadId: u.uploadID,
			ExpectedBucketOwner:  nil,
			IfMatchInitiatedTime: nil,
			RequestPayer:         "",
		},
	)
}

// uploadParts dispatches UploadPart calls concurrently; gctx cancels the in-flight ones
// as soon as the first fails.
func (u *multipartUpload) uploadParts(ctx context.Context, body io.Reader) error {
	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(maxUploadConcurrency)

	for partNum := int32(1); ; partNum++ {
		done, err := u.dispatchPart(gctx, group, body, partNum)
		if err != nil {
			_ = group.Wait()

			return err
		}

		if done {
			return group.Wait()
		}
	}
}

// dispatchPart reads the next part and starts its upload, reporting the body's end.
// Each part gets its own buffer: concurrent UploadPart goroutines must not share one.
// g.Go blocks once maxUploadConcurrency uploads are in flight, so at most that many
// buffers (plus the one being filled) are live at once.
func (u *multipartUpload) dispatchPart(
	ctx context.Context, group *errgroup.Group, body io.Reader, part int32,
) (bool, error) {
	buf := make([]byte, u.c.threshold)
	n, err := io.ReadFull(body, buf)

	if n > 0 {
		group.Go(func() error { return u.uploadPart(ctx, part, buf[:n]) })
	}

	if apperr.StdIs(err, io.EOF) || apperr.StdIs(err, io.ErrUnexpectedEOF) {
		return true, nil
	}

	if err != nil {
		return false, apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("reading multipart body %s/%s", u.bucket, u.key),
		)
	}

	return false, nil
}

// uploadPart sends one part and records it.
func (u *multipartUpload) uploadPart(ctx context.Context, part int32, data []byte) error {
	var uploadPartInput s3.UploadPartInput

	uploadPartInput.Bucket = aws.String(u.bucket)
	uploadPartInput.Key = aws.String(u.key)
	uploadPartInput.UploadId = u.uploadID
	uploadPartInput.PartNumber = aws.Int32(part)
	uploadPartInput.Body = bytes.NewReader(data)

	out, err := u.c.api.UploadPart(ctx, &uploadPartInput)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 upload part %d %s/%s", part, u.bucket, u.key),
		)
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	var completedPart types.CompletedPart

	completedPart.ETag = out.ETag
	completedPart.PartNumber = aws.Int32(part)

	u.parts = append(
		u.parts,
		completedPart,
	)

	return nil
}

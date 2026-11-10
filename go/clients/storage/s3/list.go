package s3

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// ListObjects returns all objects under prefix, transparently paginating.
func (c *s3Client) ListObjects(
	ctx context.Context,
	bucket, prefix string,
) ([]interfaces.StorageObject, error) {
	var listObjectsV2Input s3.ListObjectsV2Input

	listObjectsV2Input.Bucket = aws.String(bucket)
	listObjectsV2Input.Prefix = aws.String(prefix)

	pager := s3.NewListObjectsV2Paginator(c.api, &listObjectsV2Input)

	var objs []interfaces.StorageObject

	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, apperr.Wrap(
				err,
				apperr.CodeInternal,
				fmt.Sprintf(textS3ListSS, bucket, prefix),
			)
		}

		// Grow once per page so the inner appends don't repeatedly reallocate.
		objs = slices.Grow(objs, len(page.Contents))
		for _, o := range page.Contents {
			objs = append(objs, interfaces.StorageObject{
				Key:          aws.ToString(o.Key),
				ETag:         aws.ToString(o.ETag),
				Size:         aws.ToInt64(o.Size),
				LastModified: formatTime(o.LastModified),
				ContentType:  "",
			})
		}
	}

	return objs, nil
}

// ListObjectsPage returns at most limit objects under prefix in a SINGLE ListObjectsV2
// request (MaxKeys), the bounded counterpart of ListObjects — a cheap reachability/count
// probe rather than a full, fully-paginated inventory. A non-positive limit lets S3 apply
// its default page size.
func (c *s3Client) ListObjectsPage(
	ctx context.Context,
	bucket, prefix string,
	limit int,
) ([]interfaces.StorageObject, error) {
	var sdkListObjectsV2Input s3.ListObjectsV2Input

	sdkListObjectsV2Input.Bucket = aws.String(bucket)
	sdkListObjectsV2Input.Prefix = aws.String(prefix)

	listObjectsV2Input := &sdkListObjectsV2Input

	if limit > 0 {
		n := min(limit, math.MaxInt32)

		listObjectsV2Input.MaxKeys = aws.Int32(int32(n))
	}

	page, err := c.api.ListObjectsV2(ctx, listObjectsV2Input)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf(textS3ListSS, bucket, prefix),
		)
	}

	objs := make([]interfaces.StorageObject, 0, len(page.Contents))
	for _, o := range page.Contents {
		objs = append(objs, interfaces.StorageObject{
			Key:          aws.ToString(o.Key),
			ETag:         aws.ToString(o.ETag),
			Size:         aws.ToInt64(o.Size),
			LastModified: formatTime(o.LastModified),
			ContentType:  "",
		})
	}

	return objs, nil
}

// ListObjectsPageToken lists at most limit objects under prefix starting after
// continuationToken and returns the page plus the token to resume from ("" when the
// listing is exhausted) — the resumable, memory-bounded streaming primitive a
// large-bucket crawl pages over (it holds one page at a time and checkpoints the token to
// crash-resume). An empty continuationToken starts the listing; a non-positive limit lets
// S3 apply its default page size.
func (c *s3Client) ListObjectsPageToken(
	ctx context.Context,
	bucket, prefix, continuationToken string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	var sdkListObjectsV2Input s3.ListObjectsV2Input

	sdkListObjectsV2Input.Bucket = aws.String(bucket)
	sdkListObjectsV2Input.Prefix = aws.String(prefix)

	listObjectsV2Input := &sdkListObjectsV2Input
	if continuationToken != "" {
		listObjectsV2Input.ContinuationToken = aws.String(continuationToken)
	}

	if limit > 0 {
		n := min(limit, math.MaxInt32)

		listObjectsV2Input.MaxKeys = aws.Int32(int32(n))
	}

	page, err := c.api.ListObjectsV2(ctx, listObjectsV2Input)
	if err != nil {
		return nil, "", apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf(textS3ListSS, bucket, prefix),
		)
	}

	objs := make([]interfaces.StorageObject, 0, len(page.Contents))
	for _, o := range page.Contents {
		objs = append(objs, interfaces.StorageObject{
			Key:          aws.ToString(o.Key),
			ETag:         aws.ToString(o.ETag),
			Size:         aws.ToInt64(o.Size),
			LastModified: formatTime(o.LastModified),
			ContentType:  "",
		})
	}

	// IsTruncated + NextContinuationToken together signal "more pages"; when not
	// truncated the token is absent, so nextToken == "" cleanly terminates the caller's
	// loop.
	next := ""
	if aws.ToBool(page.IsTruncated) {
		next = aws.ToString(page.NextContinuationToken)
	}

	return objs, next, nil
}

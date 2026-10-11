package s3

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// EnsureBucket creates the named bucket if it does not already exist,
// idempotently: it probes with HeadBucket, creates the bucket when absent, and
// tolerates the already-exists/already-owned races so concurrent callers both
// succeed. It exists so local/dev provisioning — e.g. a seeding tool —
// can guarantee a bucket exists before anything writes to it, rather than
// surfacing NoSuchBucket only on the first write.
func (c *s3Client) EnsureBucket(ctx context.Context, bucket string) error {
	if _, err := c.api.HeadBucket(
		ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)},
	); err == nil {
		return nil // already exists and reachable
	}

	if _, err := c.api.CreateBucket(
		ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)},
	); err != nil {
		var owned *s3types.BucketAlreadyOwnedByYou
		var exists *s3types.BucketAlreadyExists
		if apperr.As(err, &owned) || apperr.As(err, &exists) {
			return nil // created concurrently — idempotent success
		}
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("create bucket %q", bucket),
		)
	}
	return nil
}

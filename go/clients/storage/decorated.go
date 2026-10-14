package storage

import (
	"context"
	"io"
	"sync"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs every operation of base through the client tier as the client named
// name. The AWS SDK owns wire-level retry, so retry stays off unless deps enables it;
// operations that consume a body or stream or are not idempotent (Upload, Download,
// Create/CompleteMultipartUpload) are never replayed.
func Decorate(
	base interfaces.StorageClient,
	name string,
	deps decorate.Deps,
) interfaces.StorageClient {
	return &decorator{base: base, Base: client.NewBase(name, deps)}
}

// decorator runs each StorageClient operation through the client tier.
type decorator struct {
	// base is the wrapped StorageClient whose operations are instrumented.
	base interfaces.StorageClient

	// Base runs each operation through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies StorageClient.
var _ interfaces.StorageClient = (*decorator)(nil)

// Upload streams a body to S3; non-retryable (the body cannot be replayed).
func (d *decorator) Upload(
	ctx context.Context,
	bucket, key string,
	body io.Reader,
	contentType string,
) error {
	return d.DoOnce(ctx, "upload", func(c context.Context) error {
		return d.base.Upload(c, bucket, key, body, contentType)
	})
}

// Download returns a lazy stream read after this method returns, so it runs
// through Stream: the client timeout stays alive until the
// returned body is closed (cancelling on return would abort the read).
func (d *decorator) Download(
	ctx context.Context,
	bucket, key string,
) (io.ReadCloser, error) {
	rc, cleanup, err := d.Stream(ctx, "download",
		func(c context.Context) (io.ReadCloser, error) {
			return d.base.Download(c, bucket, key)
		})
	if err != nil {
		return nil, err
	}
	return &cleanupReadCloser{ReadCloser: rc, cleanup: cleanup}, nil
}

// cleanupReadCloser runs cleanup exactly once when the wrapped stream is closed,
// tying the download's timeout/span lifetime to the consumer's read lifetime.
type cleanupReadCloser struct {
	// ReadCloser is the wrapped object body stream.
	io.ReadCloser

	// cleanup ends the operation's timeout/span; run once on Close.
	cleanup func()

	// once guards cleanup so it runs exactly once.
	once sync.Once
}

// Close closes the underlying stream, then runs the cleanup exactly once.
func (c *cleanupReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.once.Do(c.cleanup)
	return apperr.Wrap(
		err,
		apperr.CodeOr(err, apperr.CodeInternal),
		"close download stream",
	)
}

// Delete removes an object; idempotent, so retryable.
func (d *decorator) Delete(ctx context.Context, bucket, key string) error {
	return d.Do(ctx, "delete", func(c context.Context) error {
		return d.base.Delete(c, bucket, key)
	})
}

// DeleteBatch removes objects; idempotent, so retryable.
func (d *decorator) DeleteBatch(ctx context.Context, bucket string, keys []string) error {
	return d.Do(ctx, "delete_batch", func(c context.Context) error {
		return d.base.DeleteBatch(c, bucket, keys)
	})
}

// Exists reports object presence; a read, so retryable.
func (d *decorator) Exists(ctx context.Context, bucket, key string) (bool, error) {
	return d.Exec(ctx, "exists",
		func(c context.Context) (bool, error) {
			return d.base.Exists(c, bucket, key)
		})
}

// Stat returns object metadata; a read, so retryable.
func (d *decorator) Stat(
	ctx context.Context,
	bucket, key string,
) (interfaces.StorageObject, error) {
	return d.Exec(ctx, "stat",
		func(c context.Context) (interfaces.StorageObject, error) {
			return d.base.Stat(c, bucket, key)
		})
}

// PresignURL signs a GET URL; local signing (no network mutation), so retryable.
func (d *decorator) PresignURL(
	ctx context.Context,
	bucket, key string,
	expirySeconds int,
	displayFilename string,
) (string, error) {
	return d.Exec(ctx, "presign_get",
		func(c context.Context) (string, error) {
			return d.base.PresignURL(c, bucket, key, expirySeconds, displayFilename)
		})
}

// PresignPutURL signs a PUT URL; local signing, so retryable.
func (d *decorator) PresignPutURL(
	ctx context.Context,
	bucket, key string,
	expirySeconds int,
	contentType string,
) (string, error) {
	return d.Exec(ctx, "presign_put",
		func(c context.Context) (string, error) {
			return d.base.PresignPutURL(c, bucket, key, expirySeconds, contentType)
		})
}

// CreateMultipartUpload begins a multipart upload; non-retryable (each call
// creates a distinct upload id, so a retry would orphan uploads).
func (d *decorator) CreateMultipartUpload(
	ctx context.Context,
	bucket, key, contentType string,
) (string, error) {
	return d.Once(ctx, "create_multipart",
		func(c context.Context) (string, error) {
			return d.base.CreateMultipartUpload(c, bucket, key, contentType)
		})
}

// PresignUploadPart signs an upload-part URL; local signing, so retryable.
func (d *decorator) PresignUploadPart(
	ctx context.Context,
	bucket, key, uploadID string,
	partNumber int32,
	expirySeconds int,
) (string, error) {
	return d.Exec(ctx, "presign_upload_part",
		func(c context.Context) (string, error) {
			return d.base.PresignUploadPart(
				c,
				bucket,
				key,
				uploadID,
				partNumber,
				expirySeconds,
			)
		})
}

// CompleteMultipartUpload finalizes a multipart upload; non-retryable (double
// completion is an error).
func (d *decorator) CompleteMultipartUpload(
	ctx context.Context,
	bucket, key, uploadID string,
	parts []interfaces.CompletedPart,
) error {
	return d.DoOnce(ctx, "complete_multipart", func(c context.Context) error {
		return d.base.CompleteMultipartUpload(
			c,
			bucket,
			key,
			uploadID,
			parts,
		)
	})
}

// AbortMultipartUpload cancels a multipart upload; idempotent, so retryable.
func (d *decorator) AbortMultipartUpload(
	ctx context.Context,
	bucket, key, uploadID string,
) error {
	return d.Do(ctx, "abort_multipart", func(c context.Context) error {
		return d.base.AbortMultipartUpload(c, bucket, key, uploadID)
	})
}

// ListMultipartUploads lists in-progress uploads; a read, so retryable.
func (d *decorator) ListMultipartUploads(
	ctx context.Context,
	bucket string,
) ([]interfaces.MultipartUpload, error) {
	return d.Exec(ctx, "list_multipart",
		func(c context.Context) ([]interfaces.MultipartUpload, error) {
			return d.base.ListMultipartUploads(c, bucket)
		})
}

// ListObjects lists objects under a prefix; a read, so retryable.
func (d *decorator) ListObjects(
	ctx context.Context,
	bucket, prefix string,
) ([]interfaces.StorageObject, error) {
	return d.Exec(ctx, "list",
		func(c context.Context) ([]interfaces.StorageObject, error) {
			return d.base.ListObjects(c, bucket, prefix)
		})
}

// ListObjectsPage lists a bounded page of objects through the client tier.
func (d *decorator) ListObjectsPage(
	ctx context.Context,
	bucket, prefix string,
	limit int,
) ([]interfaces.StorageObject, error) {
	return d.Exec(ctx, "list_page",
		func(c context.Context) ([]interfaces.StorageObject, error) {
			return d.base.ListObjectsPage(c, bucket, prefix, limit)
		})
}

// listPageResult bundles a token-paginated list's two results so the single-value
// Exec carries both the page and the resume token through the client tier.
type listPageResult struct {
	// next is the continuation token for the following page ("" when exhausted).
	next string
	// objects is the page of storage objects.
	objects []interfaces.StorageObject
}

// ListObjectsPageToken lists a token-paginated page through the client tier.
func (d *decorator) ListObjectsPageToken(
	ctx context.Context,
	bucket, prefix, continuationToken string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	res, err := d.Exec(ctx, "list_page_token",
		func(c context.Context) (listPageResult, error) {
			objs, next, listErr := d.base.ListObjectsPageToken(
				c, bucket, prefix, continuationToken, limit,
			)
			return listPageResult{objects: objs, next: next}, listErr
		})
	return res.objects, res.next, err
}

// EnsureBucket creates the bucket if absent; idempotent, so retryable.
func (d *decorator) EnsureBucket(ctx context.Context, bucket string) error {
	return d.Do(ctx, "ensure_bucket", func(c context.Context) error {
		return d.base.EnsureBucket(c, bucket)
	})
}

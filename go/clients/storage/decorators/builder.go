// Package decorators wraps a base StorageClient with the shared client-boundary
// resilience stack (go/clients/decorators): Bulkhead → Retry → CircuitBreaker
// → Timeout → Tracing → Metrics → Logging. It delegates each operation to that
// stack's Run/RunStream so S3 gets the same protection as the other direct-SDK
// clients (DRY — one stack, every client).
//
// Retry defaults off because the AWS SDK already owns wire-level retry; a per-op
// Retryable flag governs the stack's Retry layer when an operator enables it, and
// body- or stream-consuming and non-idempotent operations (Upload, Download,
// Create/CompleteMultipartUpload) are marked non-retryable so they are never
// replayed.
package decorators

import (
	"context"
	"io"
	"sync"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"

	clientstack "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// retryable returns the per-operation flags that make an operation eligible for
// the stack's Retry layer.
func retryable() clientstack.RunOpts { return clientstack.RunOpts{Retryable: true} }

// nonRetryable returns the per-operation flags that keep the stack's Retry layer
// off for an operation.
func nonRetryable() clientstack.RunOpts { return clientstack.RunOpts{Retryable: false} }

// Builder composes the shared resilience stack around a base StorageClient.
type Builder struct {
	// base is the wrapped StorageClient the stack delegates to.
	base interfaces.StorageClient
	// metrics records per-operation counts, errors, and latency; nil disables the layer.
	metrics interfaces.Metrics
	// cb gates each operation, failing fast when open; nil disables the layer.
	cb interfaces.CircuitBreaker
	// bulkhead bounds in-flight concurrency; nil disables the layer.
	bulkhead interfaces.Bulkhead
	// retrier retries retryable operations per its policy; nil disables the layer.
	retrier interfaces.Retrier
	// logger logs operation errors; nil disables the layer.
	logger interfaces.Logger
	// tracer opens a span per operation; nil disables the layer.
	tracer oteltrace.Tracer
	// name labels the client in metrics and traces.
	name string
	// timeout bounds each operation; zero disables the layer.
	timeout time.Duration
}

// NewBuilder creates a builder wrapping base, labelled name in metrics/traces.
func NewBuilder(base interfaces.StorageClient, name string) *Builder {
	return &Builder{base: base, name: name}
}

// WithMetrics records per-operation counts, errors, and latency.
func (b *Builder) WithMetrics(m interfaces.Metrics) *Builder { b.metrics = m; return b }

// WithCircuitBreaker gates each operation, failing fast when the breaker is open.
func (b *Builder) WithCircuitBreaker(
	cb interfaces.CircuitBreaker,
) *Builder {
	b.cb = cb
	return b
}

// WithBulkhead bounds the number of concurrently in-flight operations.
func (b *Builder) WithBulkhead(
	bh interfaces.Bulkhead,
) *Builder {
	b.bulkhead = bh
	return b
}

// WithRetrier enables the Retry layer for Retryable operations (off by default —
// the AWS SDK owns wire-level retry).
func (b *Builder) WithRetrier(r interfaces.Retrier) *Builder { b.retrier = r; return b }

// WithLogger logs each failed operation with its op name and the storage error.
func (b *Builder) WithLogger(l interfaces.Logger) *Builder { b.logger = l; return b }

// WithTracer emits an OpenTelemetry span per operation, recording errors on it.
func (b *Builder) WithTracer(t oteltrace.Tracer) *Builder { b.tracer = t; return b }

// WithTimeout applies a per-operation deadline via context.
func (b *Builder) WithTimeout(d time.Duration) *Builder { b.timeout = d; return b }

// Build constructs the decorated StorageClient backed by the shared stack.
func (b *Builder) Build() interfaces.StorageClient {
	stack := clientstack.New(b.name).
		WithBulkhead(b.bulkhead).
		WithRetrier(b.retrier).
		WithCircuitBreaker(b.cb).
		WithTimeout(b.timeout).
		WithLogger(b.logger).
		WithTracer(b.tracer).
		WithMetrics(b.metrics)
	return &decorator{base: b.base, stack: stack}
}

// DecorateFromConfig wraps base with the shared resilience stack built from cfg +
// deps (the app-wiring entrypoint). A disabled cfg yields a passthrough stack, so
// the returned client behaves exactly like base — the backwards-compatible path.
func DecorateFromConfig(
	base interfaces.StorageClient,
	name string,
	cfg clientstack.Config,
	deps clientstack.Deps,
) (interfaces.StorageClient, error) {
	stack, err := clientstack.StackFromConfig(name, cfg, deps)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, stack: stack}, nil
}

// decorator delegates each StorageClient operation to the shared resilience stack.
type decorator struct {
	// base is the wrapped StorageClient whose operations are instrumented.
	base interfaces.StorageClient

	// stack is the shared resilience+observability stack each op runs through.
	stack *clientstack.Stack
}

// Compile-time assertion that decorator satisfies StorageClient.
var _ interfaces.StorageClient = (*decorator)(nil)

// noResult is the unit type for error-only operations.
type noResult = struct{}

// Upload streams a body to S3; non-retryable (the body cannot be replayed).
func (d *decorator) Upload(
	ctx context.Context,
	bucket, key string,
	body io.Reader,
	contentType string,
) error {
	_, err := clientstack.Run(ctx, d.stack, "upload", nonRetryable(),
		func(c context.Context) (noResult, error) {
			return noResult{}, d.base.Upload(c, bucket, key, body, contentType)
		})
	return err
}

// Download returns a lazy stream read after this method returns, so it runs
// through the stack's RunStream: the per-op timeout and span stay alive until the
// returned body is closed (cancelling on return would abort the read).
func (d *decorator) Download(
	ctx context.Context,
	bucket, key string,
) (io.ReadCloser, error) {
	rc, cleanup, err := clientstack.RunStream(ctx, d.stack, "download",
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
	_, err := clientstack.Run(ctx, d.stack, "delete", retryable(),
		func(c context.Context) (noResult, error) {
			return noResult{}, d.base.Delete(c, bucket, key)
		})
	return err
}

// DeleteBatch removes objects; idempotent, so retryable.
func (d *decorator) DeleteBatch(ctx context.Context, bucket string, keys []string) error {
	_, err := clientstack.Run(ctx, d.stack, "delete_batch", retryable(),
		func(c context.Context) (noResult, error) {
			return noResult{}, d.base.DeleteBatch(c, bucket, keys)
		})
	return err
}

// Exists reports object presence; a read, so retryable.
func (d *decorator) Exists(ctx context.Context, bucket, key string) (bool, error) {
	return clientstack.Run(ctx, d.stack, "exists", retryable(),
		func(c context.Context) (bool, error) {
			return d.base.Exists(c, bucket, key)
		})
}

// Stat returns object metadata; a read, so retryable.
func (d *decorator) Stat(
	ctx context.Context,
	bucket, key string,
) (interfaces.StorageObject, error) {
	return clientstack.Run(ctx, d.stack, "stat", retryable(),
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
	return clientstack.Run(ctx, d.stack, "presign_get", retryable(),
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
	return clientstack.Run(ctx, d.stack, "presign_put", retryable(),
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
	return clientstack.Run(ctx, d.stack, "create_multipart", nonRetryable(),
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
	return clientstack.Run(ctx, d.stack, "presign_upload_part", retryable(),
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
	_, err := clientstack.Run(ctx, d.stack, "complete_multipart", nonRetryable(),
		func(c context.Context) (noResult, error) {
			return noResult{}, d.base.CompleteMultipartUpload(
				c,
				bucket,
				key,
				uploadID,
				parts,
			)
		})
	return err
}

// AbortMultipartUpload cancels a multipart upload; idempotent, so retryable.
func (d *decorator) AbortMultipartUpload(
	ctx context.Context,
	bucket, key, uploadID string,
) error {
	_, err := clientstack.Run(ctx, d.stack, "abort_multipart", retryable(),
		func(c context.Context) (noResult, error) {
			return noResult{}, d.base.AbortMultipartUpload(c, bucket, key, uploadID)
		})
	return err
}

// ListMultipartUploads lists in-progress uploads; a read, so retryable.
func (d *decorator) ListMultipartUploads(
	ctx context.Context,
	bucket string,
) ([]interfaces.MultipartUpload, error) {
	return clientstack.Run(ctx, d.stack, "list_multipart", retryable(),
		func(c context.Context) ([]interfaces.MultipartUpload, error) {
			return d.base.ListMultipartUploads(c, bucket)
		})
}

// ListObjects lists objects under a prefix; a read, so retryable.
func (d *decorator) ListObjects(
	ctx context.Context,
	bucket, prefix string,
) ([]interfaces.StorageObject, error) {
	return clientstack.Run(ctx, d.stack, "list", retryable(),
		func(c context.Context) ([]interfaces.StorageObject, error) {
			return d.base.ListObjects(c, bucket, prefix)
		})
}

// ListObjectsPage lists a bounded page of objects through the resilience/observability
// stack.
func (d *decorator) ListObjectsPage(
	ctx context.Context,
	bucket, prefix string,
	limit int,
) ([]interfaces.StorageObject, error) {
	return clientstack.Run(ctx, d.stack, "list_page", retryable(),
		func(c context.Context) ([]interfaces.StorageObject, error) {
			return d.base.ListObjectsPage(c, bucket, prefix, limit)
		})
}

// listPageResult bundles a token-paginated list's two results so the single-value
// clientstack.Run carries both the page and the resume token through the decorator stack.
type listPageResult struct {
	// next is the continuation token for the following page ("" when exhausted).
	next string
	// objects is the page of storage objects.
	objects []interfaces.StorageObject
}

// ListObjectsPageToken lists a token-paginated page through the resilience/observability
// stack.
func (d *decorator) ListObjectsPageToken(
	ctx context.Context,
	bucket, prefix, continuationToken string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	res, err := clientstack.Run(ctx, d.stack, "list_page_token", retryable(),
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
	_, err := clientstack.Run(ctx, d.stack, "ensure_bucket", retryable(),
		func(c context.Context) (noResult, error) {
			return noResult{}, d.base.EnsureBucket(c, bucket)
		})
	return err
}

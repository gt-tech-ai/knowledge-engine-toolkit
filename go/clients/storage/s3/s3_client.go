package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/lifecycle"
)

// defaultMultipartThreshold is the object size (bytes) at or below which a
// single-part PutObject is used; larger bodies use the multipart uploader.
const defaultMultipartThreshold int64 = 5 * 1024 * 1024 // 5 MiB

// maxDeleteBatchKeys is S3 DeleteObjects's hard per-request limit (1000 keys).
const maxDeleteBatchKeys = 1000

// maxDeleteConcurrency bounds how many DeleteObjects chunks run at once.
const maxDeleteConcurrency = 8

// maxUploadConcurrency bounds how many multipart part uploads run at once,
// matching the AWS SDK upload manager's default upload concurrency. Parts are
// read sequentially (an io.Reader can't be seeked) but uploaded in parallel, so a
// large document's part PUTs overlap instead of running strictly serially.
const maxUploadConcurrency = 5

// API is the subset of the aws-sdk-go-v2 S3 client used by s3Client. It is an
// exported, injectable seam (Config.API) so unit tests can drive it with a generated
// mock without network access — mirroring the SQS client's API seam. *s3.Client and
// the ListObjectsV2 paginator client both satisfy it.
//
// SDK seam — mocks the AWS S3 SDK client (aws-sdk-go-v2/service/s3), cannot compose with
// a core port.
type API interface {
	Objects
	Multipart
	Buckets
}

// Objects is the single-object and listing subset of the S3 SDK.
//
// SDK seam — mirrors the AWS S3 SDK client, so it cannot compose core interfaces.
type Objects interface {
	// PutObject stores a single object in one request.
	PutObject(
		ctx context.Context,
		in *s3.PutObjectInput,
		optFns ...func(*s3.Options),
	) (*s3.PutObjectOutput, error)

	// GetObject retrieves an object's body and metadata.
	GetObject(
		ctx context.Context,
		in *s3.GetObjectInput,
		optFns ...func(*s3.Options),
	) (*s3.GetObjectOutput, error)

	// HeadObject retrieves an object's metadata without its body.
	HeadObject(
		ctx context.Context,
		in *s3.HeadObjectInput,
		optFns ...func(*s3.Options),
	) (*s3.HeadObjectOutput, error)

	// DeleteObject removes a single object by key.
	DeleteObject(
		ctx context.Context,
		in *s3.DeleteObjectInput,
		optFns ...func(*s3.Options),
	) (*s3.DeleteObjectOutput, error)

	// DeleteObjects removes multiple objects in one request.
	DeleteObjects(
		ctx context.Context,
		in *s3.DeleteObjectsInput,
		optFns ...func(*s3.Options),
	) (*s3.DeleteObjectsOutput, error)

	// ListObjectsV2 lists a page of objects; used by the paginator.
	ListObjectsV2(
		ctx context.Context,
		in *s3.ListObjectsV2Input,
		optFns ...func(*s3.Options),
	) (*s3.ListObjectsV2Output, error)
}

// Multipart is the multipart-upload subset of the S3 SDK.
//
// SDK seam — mirrors the AWS S3 SDK client, so it cannot compose core interfaces.
type Multipart interface {
	// CreateMultipartUpload begins a multipart upload and returns its upload ID.
	CreateMultipartUpload(
		ctx context.Context,
		in *s3.CreateMultipartUploadInput,
		optFns ...func(*s3.Options),
	) (*s3.CreateMultipartUploadOutput, error)

	// UploadPart uploads one part of an in-progress multipart upload.
	UploadPart(
		ctx context.Context,
		in *s3.UploadPartInput,
		optFns ...func(*s3.Options),
	) (*s3.UploadPartOutput, error)

	// CompleteMultipartUpload finalizes a multipart upload from its parts.
	CompleteMultipartUpload(
		ctx context.Context,
		in *s3.CompleteMultipartUploadInput,
		optFns ...func(*s3.Options),
	) (*s3.CompleteMultipartUploadOutput, error)

	// AbortMultipartUpload discards an in-progress multipart upload.
	AbortMultipartUpload(
		ctx context.Context,
		in *s3.AbortMultipartUploadInput,
		optFns ...func(*s3.Options),
	) (*s3.AbortMultipartUploadOutput, error)

	// ListMultipartUploads lists in-progress multipart uploads; used by the
	// paginator to find abandoned uploads for cleanup.
	ListMultipartUploads(
		ctx context.Context,
		in *s3.ListMultipartUploadsInput,
		optFns ...func(*s3.Options),
	) (*s3.ListMultipartUploadsOutput, error)
}

// Buckets is the bucket-management subset of the S3 SDK.
//
// SDK seam — mirrors the AWS S3 SDK client, so it cannot compose core interfaces.
type Buckets interface {
	// HeadBucket probes whether a bucket exists and is reachable.
	HeadBucket(
		ctx context.Context,
		in *s3.HeadBucketInput,
		optFns ...func(*s3.Options),
	) (*s3.HeadBucketOutput, error)

	// CreateBucket creates a bucket.
	CreateBucket(
		ctx context.Context,
		in *s3.CreateBucketInput,
		optFns ...func(*s3.Options),
	) (*s3.CreateBucketOutput, error)
}

// s3Client implements interfaces.StorageClient against an S3-compatible backend.
type s3Client struct {
	// NoOp supplies the no-op Start/Stop: the S3 SDK client is stateless (each PutObject/
	// GetObject is an independent request), so there is no persistent connection to open
	// or close.
	lifecycle.NoOp

	// api is the underlying S3 API (a real *s3.Client in production, a mock in tests).
	api API

	// presign generates time-limited GET/PUT URLs for direct browser access.
	presign *s3.PresignClient

	// presignEndpoint, when non-empty, overrides the signed URL's host for presigned
	// URLs only (the browser-reachable endpoint). Empty ⇒ the URL uses the client's
	// own endpoint. See S3Config.PublicEndpoint.
	presignEndpoint string

	// sseKMSKeyID, when non-empty, is the KMS key every write encrypts with (SSE-KMS).
	sseKMSKeyID string

	// threshold is the object size (bytes) above which multipart upload is used.
	threshold int64
}

// NewClient wraps a concrete aws-sdk-go-v2 S3 client as a StorageClient, building
// the presign client from it. presignEndpoint (may be empty) is the browser-reachable
// host baked into presigned URLs instead of the in-cluster endpoint. sseKMSKeyID (may
// be empty) is the KMS key every write encrypts with.
func NewClient(
	c *s3.Client,
	presignEndpoint, sseKMSKeyID string,
) interfaces.StorageClient {
	return &s3Client{
		api:             c,
		presign:         s3.NewPresignClient(c),
		presignEndpoint: presignEndpoint,
		threshold:       defaultMultipartThreshold,
		sseKMSKeyID:     sseKMSKeyID,
		NoOp:            lifecycle.NoOp{},
	}
}

// Upload stores an object, transparently choosing single-part PutObject for small
// bodies and multipart upload for bodies larger than the threshold. The body is
// peeked (not fully buffered) to make the decision.
func (c *s3Client) Upload(
	ctx context.Context,
	bucket, key string,
	body io.Reader,
	contentType string,
) error {
	head, multipart, err := classifyBody(body, c.threshold)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("reading upload body for %s/%s", bucket, key),
		)
	}

	if !multipart {
		return c.putSingle(ctx, bucket, key, bytes.NewReader(head), contentType)
	}

	return c.putMultipart(
		ctx,
		bucket,
		key,
		io.MultiReader(bytes.NewReader(head), body),
		contentType,
	)
}

// NewClientFromAPI wraps an injected API seam (a generated mock in unit tests)
// as a StorageClient. presign is nil: presigning needs the concrete *s3.Client, so
// PresignURL/PresignPutURL are not reachable on the mock path — they are unit-tested
// offline through the real newS3Client path (NewFromConfig) and live against MinIO in
// the integration suite. A non-positive threshold falls back to the default threshold.
// sseKMSKeyID (may be empty) is the KMS key every write encrypts with.
func NewClientFromAPI(
	api API,
	threshold int64,
	sseKMSKeyID string,
) interfaces.StorageClient {
	if threshold <= 0 {
		threshold = defaultMultipartThreshold
	}

	return &s3Client{
		api:             api,
		threshold:       threshold,
		sseKMSKeyID:     sseKMSKeyID,
		NoOp:            lifecycle.NoOp{},
		presign:         nil,
		presignEndpoint: "",
	}
}

// Download streams an object's body. Returns errors.ErrNotFound when absent.
func (c *s3Client) Download(
	ctx context.Context,
	bucket, key string,
) (io.ReadCloser, error) {
	var getObjectInput s3.GetObjectInput

	getObjectInput.Bucket = aws.String(bucket)
	getObjectInput.Key = aws.String(key)

	out, err := c.api.GetObject(ctx, &getObjectInput)
	if err != nil {
		if isNotFound(err) {
			return nil, apperr.NotFound(fmt.Sprintf("object %s/%s", bucket, key))
		}

		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 get %s/%s", bucket, key),
		)
	}

	return out.Body, nil
}

// classifyBody peeks up to threshold+1 bytes to decide single-part vs multipart
// without buffering the entire body. It returns the bytes read so far, whether the
// body exceeds the threshold, and any genuine read error.
func classifyBody(
	body io.Reader,
	threshold int64,
) (head []byte, multipart bool, err error) {
	// Grow a buffer up to threshold+1 bytes via io.CopyN so small uploads (the
	// common case) only allocate their own size, not a full threshold-sized buffer.
	var buf bytes.Buffer

	_, rerr := io.CopyN(&buf, body, threshold+1)

	switch {
	case apperr.StdIs(rerr, io.EOF):
		// Body smaller than threshold+1 → single-part.
		return buf.Bytes(), false, nil
	case rerr != nil:
		return nil, false, apperr.Wrap(
			rerr,
			apperr.CodeOr(rerr, apperr.CodeInternal),
			"read upload body",
		)
	default:
		// Copied threshold+1 bytes → body exceeds the threshold → multipart.
		return buf.Bytes(), true, nil
	}
}

// Delete removes a single object. S3 delete is idempotent (no error if absent).
func (c *s3Client) Delete(ctx context.Context, bucket, key string) error {
	var deleteObjectInput s3.DeleteObjectInput

	deleteObjectInput.Bucket = aws.String(bucket)
	deleteObjectInput.Key = aws.String(key)

	_, err := c.api.DeleteObject(ctx, &deleteObjectInput)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 delete %s/%s", bucket, key),
		)
	}

	return nil
}

// DeleteBatch removes multiple objects in one request.
func (c *s3Client) DeleteBatch(ctx context.Context, bucket string, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	// S3 DeleteObjects rejects a request with more than 1000 keys, so split into
	// ≤1000-key chunks. Before this a >1000-key delete failed outright. A
	// single chunk runs inline; multiple chunks run concurrently (bounded), and
	// the first chunk error is returned.
	if len(keys) <= maxDeleteBatchKeys {
		return c.deleteChunk(ctx, bucket, keys)
	}

	group, gctx := errgroup.WithContext(ctx)
	group.SetLimit(maxDeleteConcurrency)

	for chunk := range slices.Chunk(keys, maxDeleteBatchKeys) {
		group.Go(func() error { return c.deleteChunk(gctx, bucket, chunk) })
	}

	return group.Wait()
}

// Exists reports whether an object is present.
func (c *s3Client) Exists(ctx context.Context, bucket, key string) (bool, error) {
	var headObjectInput s3.HeadObjectInput

	headObjectInput.Bucket = aws.String(bucket)
	headObjectInput.Key = aws.String(key)

	_, err := c.api.HeadObject(ctx, &headObjectInput)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}

		return false, apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 head %s/%s", bucket, key),
		)
	}

	return true, nil
}

// Stat returns object metadata without downloading the body.
func (c *s3Client) Stat(
	ctx context.Context,
	bucket, key string,
) (interfaces.StorageObject, error) {
	var headObjectInput s3.HeadObjectInput

	headObjectInput.Bucket = aws.String(bucket)
	headObjectInput.Key = aws.String(key)

	out, err := c.api.HeadObject(ctx, &headObjectInput)
	if err != nil {
		if isNotFound(err) {
			return interfaces.StorageObject{}, apperr.NotFound(
				fmt.Sprintf("object %s/%s", bucket, key),
			)
		}

		return interfaces.StorageObject{}, apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 head %s/%s", bucket, key),
		)
	}

	return interfaces.StorageObject{
		Key:          key,
		ContentType:  aws.ToString(out.ContentType),
		ETag:         aws.ToString(out.ETag),
		Revision:     "",
		Size:         aws.ToInt64(out.ContentLength),
		LastModified: formatTime(out.LastModified),
	}, nil
}

// PresignURL returns a presigned GET URL valid for expirySeconds. A non-empty
// displayFilename signs a response-content-disposition override so S3 serves the
// object as an attachment (download) under that name rather than rendering it inline.
func (c *s3Client) PresignURL(
	ctx context.Context,
	bucket, key string,
	expirySeconds int,
	displayFilename string,
) (string, error) {
	var sdkGetObjectInput s3.GetObjectInput

	sdkGetObjectInput.Bucket = aws.String(bucket)
	sdkGetObjectInput.Key = aws.String(key)

	getObjectInput := &sdkGetObjectInput
	if displayFilename != "" {
		getObjectInput.ResponseContentDisposition = aws.String(
			contentDispositionAttachment(displayFilename),
		)
	}

	req, err := c.presign.PresignGetObject(
		ctx,
		getObjectInput,
		c.presignOpts(expirySeconds),
	)
	if err != nil {
		return "", apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("presign get %s/%s", bucket, key),
		)
	}

	return req.URL, nil
}

// PresignPutURL returns a presigned PUT URL valid for expirySeconds, pinning
// Content-Type.
func (c *s3Client) PresignPutURL(
	ctx context.Context,
	bucket, key string,
	expirySeconds int,
	contentType string,
) (string, error) {
	var putObjectInput s3.PutObjectInput

	putObjectInput.Bucket = aws.String(bucket)
	putObjectInput.Key = aws.String(key)
	putObjectInput.ContentType = aws.String(contentType)

	req, err := c.presign.PresignPutObject(
		ctx,
		&putObjectInput,
		c.presignOpts(expirySeconds),
	)
	if err != nil {
		return "", apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("presign put %s/%s", bucket, key),
		)
	}

	return req.URL, nil
}

// contentDispositionAttachment builds an RFC 6266 Content-Disposition value that
// forces a download under filename. It emits both a sanitized ASCII `filename=` (the
// legacy fallback: quotes/backslashes/control characters stripped so the quoted-string
// stays well-formed) and an RFC 5987 `filename*=UTF-8”…` with the raw name
// percent-encoded, so non-ASCII names survive in modern browsers.
func contentDispositionAttachment(filename string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r > 0x7f {
			return '_'
		}

		return r
	}, filename)

	return fmt.Sprintf(
		"attachment; filename=%q; filename*=UTF-8''%s",
		ascii,
		url.PathEscape(filename),
	)
}

// presignOpts builds the PresignOptions mutator shared by every presign call: it sets
// the URL expiry and, when a browser-reachable presignEndpoint is configured, rewrites
// the signed URL's host to it (preserving the client's path-style addressing). The
// SigV4 signature is computed over that host, so the browser PUT/GET validates.
func (c *s3Client) presignOpts(expirySeconds int) func(*s3.PresignOptions) {
	return func(o *s3.PresignOptions) {
		o.Expires = time.Duration(expirySeconds) * time.Second
		if c.presignEndpoint != "" {
			o.ClientOptions = append(o.ClientOptions, func(so *s3.Options) {
				so.BaseEndpoint = aws.String(c.presignEndpoint)
			})
		}
	}
}

// sseKMS returns the server-side-encryption fields of every write: aws:kms and the key
// id when a key is configured, else the zero values (no encryption requested).
func (c *s3Client) sseKMS() (mode types.ServerSideEncryption, keyID *string) {
	if c.sseKMSKeyID == "" {
		return "", nil
	}

	return types.ServerSideEncryptionAwsKms, aws.String(c.sseKMSKeyID)
}

// putSingle stores a small object in one request.
func (c *s3Client) putSingle(
	ctx context.Context,
	bucket, key string,
	body io.Reader,
	contentType string,
) error {
	var sdkPutObjectInput s3.PutObjectInput

	sdkPutObjectInput.Bucket = aws.String(bucket)
	sdkPutObjectInput.Key = aws.String(key)
	sdkPutObjectInput.Body = body
	sdkPutObjectInput.ContentType = aws.String(contentType)

	putObjectInput := &sdkPutObjectInput

	putObjectInput.ServerSideEncryption, putObjectInput.SSEKMSKeyId = c.sseKMS()

	_, err := c.api.PutObject(ctx, putObjectInput)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 put %s/%s", bucket, key),
		)
	}

	return nil
}

// deleteChunk deletes a single ≤1000-key batch via DeleteObjects, surfacing both
// the request error and any per-object failures reported in the response body.
func (c *s3Client) deleteChunk(ctx context.Context, bucket string, keys []string) error {
	objs := make([]types.ObjectIdentifier, len(keys))
	for i, k := range keys {
		objs[i] = types.ObjectIdentifier{
			Key:              aws.String(k),
			ETag:             nil,
			LastModifiedTime: nil,
			Size:             nil,
			VersionId:        nil,
		}
	}

	var deleteObjectsInput s3.DeleteObjectsInput

	deleteObjectsInput.Bucket = aws.String(bucket)
	deleteObjectsInput.Delete = &types.Delete{Objects: objs, Quiet: aws.Bool(true)}

	out, err := c.api.DeleteObjects(ctx, &deleteObjectsInput)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 delete batch %s (%d keys)", bucket, len(keys)),
		)
	}

	// DeleteObjects reports per-object failures in the response body even when the
	// HTTP call itself succeeds (Quiet suppresses successes, not errors). Surface
	// them so a partial batch-delete failure is not silently swallowed.
	if len(out.Errors) > 0 {
		first := out.Errors[0]

		return apperr.New(apperr.CodeInternal, fmt.Sprintf(
			"s3 delete batch %s: %d of %d objects failed (first: key=%s code=%s message=%s)",
			bucket,
			len(out.Errors),
			len(keys),
			aws.ToString(first.Key),
			aws.ToString(first.Code),
			aws.ToString(first.Message),
		))
	}

	return nil
}

// isNotFound reports whether err is an S3 "object missing" error, across the two
// distinct typed errors (GetObject's NoSuchKey, HeadObject's NotFound) plus a
// smithy APIError code fallback.
func isNotFound(err error) bool {
	var (
		nsk *types.NoSuchKey
		nf  *types.NotFound
	)

	if apperr.As(err, &nsk) || apperr.As(err, &nf) {
		return true
	}

	var apiErr smithy.APIError

	if apperr.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}

	return false
}

// formatTime renders an optional timestamp as RFC3339, or "" when nil.
func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}

	return t.Format(time.RFC3339)
}

// Compile-time assurance that s3Client satisfies the interface.
var _ interfaces.StorageClient = (*s3Client)(nil)

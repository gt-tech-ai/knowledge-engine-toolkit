package s3

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
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
// SDK seam
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
// SDK seam
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
// SDK seam
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
		api:         api,
		threshold:   threshold,
		sseKMSKeyID: sseKMSKeyID,
	}
}

// Download streams an object's body. Returns errors.ErrNotFound when absent.
func (c *s3Client) Download(
	ctx context.Context,
	bucket, key string,
) (io.ReadCloser, error) {
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
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
	_, err := c.api.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
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

// sortParts orders completed parts by ascending part number, which S3 requires;
// concurrent uploads finish in arbitrary order.
func sortParts(parts []types.CompletedPart) {
	slices.SortFunc(parts, func(a, b types.CompletedPart) int {
		return cmp.Compare(aws.ToInt32(a.PartNumber), aws.ToInt32(b.PartNumber))
	})
}

// Exists reports whether an object is present.
func (c *s3Client) Exists(ctx context.Context, bucket, key string) (bool, error) {
	_, err := c.api.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
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
	out, err := c.api.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
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
	getObjectInput := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
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
	req, err := c.presign.PresignPutObject(
		ctx,
		&s3.PutObjectInput{
			Bucket:      aws.String(bucket),
			Key:         aws.String(key),
			ContentType: aws.String(contentType),
		},
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

// CreateMultipartUpload begins a multipart upload and returns its S3 upload id for
// the browser-direct part-upload flow.
func (c *s3Client) CreateMultipartUpload(
	ctx context.Context,
	bucket, key, contentType string,
) (string, error) {
	out, err := c.api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	})
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
	req, err := c.presign.PresignUploadPart(
		ctx,
		&s3.UploadPartInput{
			Bucket:     aws.String(bucket),
			Key:        aws.String(key),
			UploadId:   aws.String(uploadID),
			PartNumber: aws.Int32(partNumber),
		},
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
	for i, p := range parts {
		completed[i] = types.CompletedPart{
			ETag:       aws.String(p.ETag),
			PartNumber: aws.Int32(p.PartNumber),
		}
	}

	// S3 requires the completed parts in ascending part-number order and rejects an
	// unordered list with InvalidPartOrder. The parts come from untrusted client input
	// (a browser may report parallel-uploaded parts in completion order), so sort
	// defensively at the S3 boundary rather than trusting the caller.
	slices.SortFunc(completed, func(a, b types.CompletedPart) int {
		return cmp.Compare(aws.ToInt32(a.PartNumber), aws.ToInt32(b.PartNumber))
	})

	_, err := c.api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
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
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
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

// ListMultipartUploads returns the in-progress multipart uploads in a bucket,
// transparently paginating.
func (c *s3Client) ListMultipartUploads(
	ctx context.Context,
	bucket string,
) ([]interfaces.MultipartUpload, error) {
	pager := s3.NewListMultipartUploadsPaginator(c.api, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(bucket),
	})

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

// ListObjects returns all objects under prefix, transparently paginating.
func (c *s3Client) ListObjects(
	ctx context.Context,
	bucket, prefix string,
) ([]interfaces.StorageObject, error) {
	pager := s3.NewListObjectsV2Paginator(c.api, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})

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
	listObjectsV2Input := &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	}

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
	listObjectsV2Input := &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	}
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
	putObjectInput := &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String(contentType),
	}

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
	createIn := &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}

	createIn.ServerSideEncryption, createIn.SSEKMSKeyId = c.sseKMS()

	created, err := c.api.CreateMultipartUpload(ctx, createIn)
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 create multipart %s/%s", bucket, key),
		)
	}

	multipartUploadValue := &multipartUpload{
		c:        c,
		uploadID: created.UploadId,
		bucket:   bucket,
		key:      key,
	}

	err = multipartUploadValue.uploadParts(ctx, body)
	if err != nil {
		multipartUploadValue.abort(ctx)

		return err
	}

	sortParts(multipartUploadValue.parts)

	_, err = c.api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: multipartUploadValue.uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: multipartUploadValue.parts,
		},
	})
	if err != nil {
		multipartUploadValue.abort(ctx)

		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 complete multipart %s/%s", bucket, key),
		)
	}

	return nil
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
	out, err := u.c.api.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(u.bucket),
		Key:        aws.String(u.key),
		UploadId:   u.uploadID,
		PartNumber: aws.Int32(part),
		Body:       bytes.NewReader(data),
	})
	if err != nil {
		return apperr.Wrap(
			err,
			apperr.CodeInternal,
			fmt.Sprintf("s3 upload part %d %s/%s", part, u.bucket, u.key),
		)
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	u.parts = append(
		u.parts,
		types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(part)},
	)

	return nil
}

// deleteChunk deletes a single ≤1000-key batch via DeleteObjects, surfacing both
// the request error and any per-object failures reported in the response body.
func (c *s3Client) deleteChunk(ctx context.Context, bucket string, keys []string) error {
	objs := make([]types.ObjectIdentifier, len(keys))
	for i, k := range keys {
		objs[i] = types.ObjectIdentifier{Key: aws.String(k)}
	}

	out, err := c.api.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
	})
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

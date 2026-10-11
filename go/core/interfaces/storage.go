package interfaces

import (
	"context"
	"io"
)

// StorageClient provides object storage operations (S3-compatible): upload
// (single-part or transparent multipart by size), download, delete (single and
// batch), existence and metadata checks, presigned GET/PUT URLs, prefix listing, and
// the presigned/browser-direct multipart control plane (create/presign-part/complete/
// abort/list) for large-file uploads. Absent objects surface as errors.ErrNotFound.
type StorageClient interface {
	// Upload stores an object with the given key in the specified bucket.
	Upload(
		ctx context.Context,
		bucket, key string,
		body io.Reader,
		contentType string,
	) error

	// Download retrieves an object by key from the specified bucket.
	Download(ctx context.Context, bucket, key string) (io.ReadCloser, error)

	// Delete removes an object by key from the specified bucket.
	Delete(ctx context.Context, bucket, key string) error

	// DeleteBatch removes multiple objects by key in a single request. It is the
	// efficient path for cleanup operations (e.g. orphaned-file reconciliation).
	DeleteBatch(ctx context.Context, bucket string, keys []string) error

	// Exists checks if an object exists at the given key.
	Exists(ctx context.Context, bucket, key string) (bool, error)

	// Stat returns metadata (content-type, size, etag, last-modified) for an object
	// without downloading its body. Returns errors.ErrNotFound when the key is absent.
	Stat(ctx context.Context, bucket, key string) (StorageObject, error)

	// PresignURL generates a pre-signed URL for temporary GET (download) access to
	// an object. The URL expires after the specified duration (in seconds).
	//
	// When displayFilename is non-empty the URL is signed with a
	// response-content-disposition of `attachment; filename="…"`, so S3 returns that
	// header on the GET and the browser downloads the object (under the given name)
	// instead of rendering it inline. An empty displayFilename leaves the disposition
	// unset (inline). The override is part of the SigV4 signature, so it must be set
	// here at presign time — a client cannot append it to the URL after the fact.
	PresignURL(
		ctx context.Context,
		bucket, key string,
		expirySeconds int,
		displayFilename string,
	) (string, error)

	// PresignPutURL generates a pre-signed URL for temporary PUT (upload) access,
	// allowing direct browser-to-storage uploads. The URL expires after the
	// specified duration (in seconds); contentType pins the upload's Content-Type.
	PresignPutURL(
		ctx context.Context,
		bucket, key string,
		expirySeconds int,
		contentType string,
	) (string, error)

	// CreateMultipartUpload begins a browser-direct multipart upload and returns its
	// S3 upload id. The caller presigns each part with PresignUploadPart and later
	// finalizes with CompleteMultipartUpload (or discards with AbortMultipartUpload).
	CreateMultipartUpload(
		ctx context.Context,
		bucket, key, contentType string,
	) (string, error)

	// PresignUploadPart generates a pre-signed PUT URL for one part (1-based
	// partNumber) of the multipart upload identified by uploadID. The URL expires
	// after expirySeconds; the browser PUTs the part bytes to it and reads the
	// returned ETag (which the bucket CORS must expose).
	PresignUploadPart(
		ctx context.Context,
		bucket, key, uploadID string,
		partNumber int32,
		expirySeconds int,
	) (string, error)

	// CompleteMultipartUpload finalizes the multipart upload identified by uploadID,
	// assembling the client-reported parts into the object at key. Parts must be
	// ordered by ascending part number.
	CompleteMultipartUpload(
		ctx context.Context,
		bucket, key, uploadID string,
		parts []CompletedPart,
	) error

	// AbortMultipartUpload discards the in-progress multipart upload identified by
	// uploadID, releasing its already-uploaded parts.
	AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error

	// ListMultipartUploads lists the in-progress (not yet completed or aborted)
	// multipart uploads in a bucket, so a cleanup sweep can abort abandoned ones —
	// which are invisible to ListObjects until completed.
	ListMultipartUploads(ctx context.Context, bucket string) ([]MultipartUpload, error)

	// ListObjects lists objects in a bucket with the given prefix.
	ListObjects(ctx context.Context, bucket, prefix string) ([]StorageObject, error)

	// ListObjectsPage lists AT MOST limit objects in a bucket with the given prefix — a
	// bounded single-page variant of ListObjects for callers that need a cheap
	// reachability/count probe rather than a full inventory (e.g. a connector's
	// TestConnection against a large customer bucket). A non-positive limit lists at most
	// one page's worth (backend default).
	ListObjectsPage(
		ctx context.Context,
		bucket, prefix string,
		limit int,
	) ([]StorageObject, error)

	// ListObjectsPageToken lists AT MOST limit objects under prefix STARTING AFTER
	// continuationToken (empty token = start of the listing) and returns the objects plus
	// the token to resume from (nextToken == "" when the listing is exhausted). It is the
	// resumable, memory-bounded streaming primitive a large-bucket crawl pages over —
	// never materializing the full key list — so a caller (e.g. a sync engine) holds at
	// most one page at a time and can checkpoint the token to crash-resume. A
	// non-positive limit lets the backend apply its default page size.
	ListObjectsPageToken(
		ctx context.Context,
		bucket, prefix, continuationToken string,
		limit int,
	) (objects []StorageObject, nextToken string, err error)

	// EnsureBucket creates the named bucket if it does not already exist,
	// idempotently (a concurrently-created bucket is treated as success). It exists
	// so local/dev provisioning — e.g. a seeding tool — can guarantee an upload
	// path has its bucket before the first presigned PUT.
	EnsureBucket(ctx context.Context, bucket string) error
}

// CompletedPart identifies one finished part of a multipart upload, pairing its
// 1-based part number with the ETag S3 returned from the UploadPart request.
type CompletedPart struct {
	// ETag is the entity tag S3 returned from the part's UploadPart request.
	ETag string `json:"etag"`
	// PartNumber is the part's 1-based index within the multipart upload.
	PartNumber int32 `json:"part_number"`
}

// MultipartUpload describes an in-progress multipart upload reported by
// ListMultipartUploads — the seam the cleanup sweep uses to find and abort
// abandoned uploads (which are invisible to ListObjects until completed).
type MultipartUpload struct {
	// Key is the object key the upload will assemble into.
	Key string `json:"key"`

	// UploadID is the S3 multipart upload id (needed to abort or complete it).
	UploadID string `json:"upload_id"`

	// Initiated is the RFC3339 timestamp the upload was created, or "" if unknown.
	Initiated string `json:"initiated"`
}

// StorageObject represents metadata about a stored object.
type StorageObject struct {
	// Key is the object's storage key (path) in the bucket.
	Key string `json:"key"`

	// ContentType is the MIME type of the object.
	ContentType string `json:"content_type"`

	// LastModified is the timestamp of the last modification.
	LastModified string `json:"last_modified"`

	// ETag is the entity tag for cache validation.
	ETag string `json:"etag"`

	// Size is the object size in bytes.
	Size int64 `json:"size"`
}

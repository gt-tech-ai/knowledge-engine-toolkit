// Package memory provides an in-memory StorageClient backend for no-infra builds and
// tests.
//
// Client keeps objects in a process-local per-bucket map so a service can build and
// unit-test the object-storage path with no S3/MinIO — the stub-first property
// (ARCHITECTURE.md#stub-first-backends). Selected by the
// storage tier Kind. The presign methods return a synthetic memory:// URL: a memory
// backend issues no real signed URLs, so it is confined to test/CI/all-stubs config and
// is never reachable from a real client-facing upload flow.
package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Compile-time interface assertion.
var _ interfaces.StorageClient = (*Client)(nil)

// object is a stored blob plus its computed metadata.
type object struct {
	// body is the stored blob's raw bytes.
	body []byte
	// meta is the object's computed metadata (size, content type, etc.).
	meta interfaces.StorageObject
}

// multipart is an in-progress multipart upload. A memory backend has no
// presigned-part-PUT path (PresignUploadPart returns a synthetic URL nothing writes to),
// so it carries no part bytes; CompleteMultipartUpload just materializes the (empty)
// object so a caller's multipart lifecycle builds and runs with no S3.
type multipart struct {
	// bucket is the destination bucket for the completed object.
	bucket string
	// key is the destination object key.
	key string
	// contentType is the content type recorded on completion.
	contentType string
}

// Client is an in-memory StorageClient (dev/test; no external storage).
type Client struct {
	// buckets maps bucket name → object key → stored object.
	buckets map[string]map[string]object
	// uploads maps an upload id → its in-progress multipart upload.
	uploads map[string]*multipart
	// seq is a monotonic counter used to synthesize upload ids.
	seq int
	// mu guards buckets, uploads, and seq.
	mu sync.Mutex
}

// New creates an empty in-memory storage client.
func New() *Client {
	return &Client{
		buckets: make(map[string]map[string]object),
		uploads: make(map[string]*multipart),
	}
}

// EnsureBucket creates the bucket if absent (idempotent).
func (c *Client) EnsureBucket(_ context.Context, bucket string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bucketOf(bucket)
	return nil
}

// Upload reads body fully and stores it under bucket/key with computed metadata.
func (c *Client) Upload(
	_ context.Context,
	bucket, key string,
	body io.Reader,
	contentType string,
) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "memory storage: read body")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.put(bucket, key, data, contentType)
	return nil
}

// Download returns a reader over the stored bytes, or a not-found error.
func (c *Client) Download(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	obj, err := c.get(bucket, key)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(obj.body)), nil
}

// Delete removes an object; a missing key is a no-op (S3 delete is idempotent).
func (c *Client) Delete(_ context.Context, bucket, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.buckets[bucket], key)
	return nil
}

// DeleteBatch removes multiple objects by key in one call.
func (c *Client) DeleteBatch(_ context.Context, bucket string, keys []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range keys {
		delete(c.buckets[bucket], key)
	}
	return nil
}

// Exists reports whether an object exists at bucket/key.
func (c *Client) Exists(_ context.Context, bucket, key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.buckets[bucket][key]
	return ok, nil
}

// Stat returns an object's metadata without its body, or a not-found error.
func (c *Client) Stat(
	_ context.Context,
	bucket, key string,
) (interfaces.StorageObject, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	obj, err := c.get(bucket, key)
	if err != nil {
		return interfaces.StorageObject{}, err
	}
	return obj.meta, nil
}

// PresignURL returns a synthetic memory:// GET URL (no real signing; expiry/filename
// ignored).
func (c *Client) PresignURL(
	_ context.Context,
	bucket, key string,
	_ int,
	_ string,
) (string, error) {
	return memoryURL(bucket, key), nil
}

// PresignPutURL returns a synthetic memory:// PUT URL (no real signing;
// expiry/content-type ignored).
func (c *Client) PresignPutURL(
	_ context.Context,
	bucket, key string,
	_ int,
	_ string,
) (string, error) {
	return memoryURL(bucket, key), nil
}

// CreateMultipartUpload begins an in-memory multipart upload and returns its id.
func (c *Client) CreateMultipartUpload(
	_ context.Context,
	bucket, key, contentType string,
) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := fmt.Sprintf("mem-upload-%d", c.seq)
	c.uploads[id] = &multipart{bucket: bucket, key: key, contentType: contentType}
	return id, nil
}

// PresignUploadPart returns a synthetic memory:// URL for one part of a multipart upload.
func (c *Client) PresignUploadPart(
	_ context.Context,
	bucket, key, uploadID string,
	partNumber int32,
	_ int,
) (string, error) {
	return fmt.Sprintf(
		"%s?uploadId=%s&partNumber=%d",
		memoryURL(bucket, key),
		uploadID,
		partNumber,
	), nil
}

// CompleteMultipartUpload finalizes the upload by writing an EMPTY object at the upload's
// bucket/key with its content type: a memory backend receives no part bytes, so parts is
// ignored and nothing is assembled. Like S3's NoSuchUpload, an unknown upload id — or a
// bucket/key other than the one the upload was created for — is CodeNotFound and writes
// nothing.
func (c *Client) CompleteMultipartUpload(
	_ context.Context,
	bucket, key, uploadID string,
	_ []interfaces.CompletedPart,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	up, ok := c.uploads[uploadID]
	if !ok || up.bucket != bucket || up.key != key {
		return apperr.New(
			apperr.CodeNotFound,
			fmt.Sprintf(
				"memory storage: upload %s not found for %s/%s",
				uploadID,
				bucket,
				key,
			),
		)
	}
	// No part bytes exist (a memory backend has no part-PUT path); materialize the empty
	// object so the multipart lifecycle completes. The client's ETags (the parts arg) are
	// irrelevant to memory.
	c.put(bucket, key, nil, up.contentType)
	delete(c.uploads, uploadID)
	return nil
}

// AbortMultipartUpload discards an in-progress multipart upload.
func (c *Client) AbortMultipartUpload(_ context.Context, _, _, uploadID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.uploads, uploadID)
	return nil
}

// ListMultipartUploads lists the in-progress multipart uploads in a bucket.
func (c *Client) ListMultipartUploads(
	_ context.Context,
	bucket string,
) ([]interfaces.MultipartUpload, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	uploads := make([]interfaces.MultipartUpload, 0)
	for id, up := range c.uploads {
		if up.bucket == bucket {
			uploads = append(
				uploads,
				interfaces.MultipartUpload{Key: up.key, UploadID: id},
			)
		}
	}
	return uploads, nil
}

// ListObjects lists the metadata of objects in a bucket whose key starts with prefix.
func (c *Client) ListObjects(
	_ context.Context,
	bucket, prefix string,
) ([]interfaces.StorageObject, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	objects := make([]interfaces.StorageObject, 0)
	for key, obj := range c.buckets[bucket] {
		if strings.HasPrefix(key, prefix) {
			objects = append(objects, obj.meta)
		}
	}
	return objects, nil
}

// ListObjectsPage lists at most limit objects whose key starts with prefix — the bounded
// counterpart of ListObjects. A non-positive limit returns all matches (the memory
// backend has no page boundary).
func (c *Client) ListObjectsPage(
	_ context.Context,
	bucket, prefix string,
	limit int,
) ([]interfaces.StorageObject, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	objects := make([]interfaces.StorageObject, 0)
	for key, obj := range c.buckets[bucket] {
		if limit > 0 && len(objects) >= limit {
			break
		}
		if strings.HasPrefix(key, prefix) {
			objects = append(objects, obj.meta)
		}
	}
	return objects, nil
}

// ListObjectsPageToken lists at most limit prefix-matching objects in key order starting
// AFTER continuationToken, returning the page plus the token to resume from ("" when
// exhausted). It sorts the matching keys so the in-memory store paginates
// deterministically like a real object store (a map has no stable iteration order),
// letting a test drive the streaming, resumable listing path.
func (c *Client) ListObjectsPageToken(
	_ context.Context,
	bucket, prefix, continuationToken string,
	limit int,
) ([]interfaces.StorageObject, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.buckets[bucket]))
	for key := range c.buckets[bucket] {
		if strings.HasPrefix(key, prefix) && key > continuationToken {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	objects := make([]interfaces.StorageObject, 0, len(keys))
	next := ""
	for _, key := range keys {
		if limit > 0 && len(objects) >= limit {
			next = objects[len(objects)-1].Key // more remain → resume after the last returned key
			break
		}
		objects = append(objects, c.buckets[bucket][key].meta)
	}
	return objects, next, nil
}

// bucketOf returns the bucket's object map, creating it on demand. Caller holds c.mu.
func (c *Client) bucketOf(bucket string) map[string]object {
	objects := c.buckets[bucket]
	if objects == nil {
		objects = make(map[string]object)
		c.buckets[bucket] = objects
	}
	return objects
}

// put stores data under bucket/key with computed metadata. Caller holds c.mu.
func (c *Client) put(bucket, key string, data []byte, contentType string) {
	sum := sha256.Sum256(data)
	c.bucketOf(bucket)[key] = object{
		body: data,
		meta: interfaces.StorageObject{
			Key:          key,
			ContentType:  contentType,
			LastModified: time.Now().UTC().Format(time.RFC3339),
			ETag:         hex.EncodeToString(sum[:]),
			Size:         int64(len(data)),
		},
	}
}

// get returns the stored object or a not-found error. Caller holds c.mu.
func (c *Client) get(bucket, key string) (object, error) {
	obj, ok := c.buckets[bucket][key]
	if !ok {
		return object{}, apperr.New(
			apperr.CodeNotFound,
			fmt.Sprintf("memory storage: %s/%s not found", bucket, key),
		)
	}
	return obj, nil
}

// memoryURL is the synthetic, unsigned URL a memory backend returns for a presign
// request.
func memoryURL(bucket, key string) string {
	return fmt.Sprintf("memory://%s/%s", bucket, key)
}

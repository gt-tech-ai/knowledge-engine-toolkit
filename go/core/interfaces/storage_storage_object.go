package interfaces

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

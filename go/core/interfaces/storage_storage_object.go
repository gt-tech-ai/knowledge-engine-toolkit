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

	// Revision is the source revision the object was listed at, so a reader fetches
	// the same content the listing saw: the pinned commit for a connector's github
	// kind. Empty for object stores, whose ETag already names the content.
	Revision string `json:"revision,omitempty"`

	// Size is the object size in bytes.
	Size int64 `json:"size"`
}

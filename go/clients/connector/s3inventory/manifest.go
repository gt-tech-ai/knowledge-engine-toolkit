package s3inventory

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// maxManifestBytes bounds the manifest read; a report's manifest lists its data files
// and is a few kilobytes even for a bucket of billions of objects.
const maxManifestBytes = 16 << 20

// formatCSV is the only report format read; ORC and Parquet need a columnar decoder.
const formatCSV = "CSV"

// The report columns the source reads (fileSchema names).
const (
	// colBucket is the source bucket of each row.
	colBucket = "Bucket"
	// colKey is the URL-encoded object key.
	colKey = "Key"
	// colSize is the object size in bytes.
	colSize = "Size"
	// colLastModified is the object's last-modified time (ISO 8601).
	colLastModified = "LastModifiedDate"
	// colETag is the object's entity tag, unquoted.
	colETag = "ETag"
	// colIsLatest is "false" for a non-current version (versioned reports).
	colIsLatest = "IsLatest"
	// colIsDeleteMarker is "true" for a delete marker (versioned reports).
	colIsDeleteMarker = "IsDeleteMarker"
)

// manifest is the slice of an S3 Inventory manifest.json the source reads.
type manifest struct {
	// SourceBucket is the bucket the report lists.
	SourceBucket string `json:"sourceBucket"`
	// FileFormat is CSV, ORC or Parquet.
	FileFormat string `json:"fileFormat"`
	// FileSchema names the data files' columns, comma-separated, in order.
	FileSchema string `json:"fileSchema"`
	// Files are the report's data files, in report order.
	Files []dataFile `json:"files"`
}

// dataFile is one report data file.
type dataFile struct {
	// Key is the data file's key in the inventory bucket.
	Key string `json:"key"`
}

// schema maps a column name to its index in a row.
type schema map[string]int

// parseManifest decodes and validates a manifest for sourceBucket: CSV only, with the
// Bucket, Key, ETag and Size columns. Bucket and Key locate each object; without ETag
// every crawl reads as changed (re-ingesting the bucket), and without Size the
// oversized-object cap never fires. A malformed or unusable report is CodeInvalidInput.
func parseManifest(body io.Reader, sourceBucket string) (*manifest, schema, error) {
	var m manifest
	if err := json.NewDecoder(io.LimitReader(body, maxManifestBytes)).
		Decode(&m); err != nil {
		return nil, nil, apperr.Wrap(
			err,
			apperr.CodeInvalidInput,
			"connector s3_inventory: decode manifest",
		)
	}
	if !strings.EqualFold(m.FileFormat, formatCSV) {
		return nil, nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"connector s3_inventory: report format %q is not supported; "+
				"configure the inventory as CSV",
			m.FileFormat,
		))
	}
	if m.SourceBucket != sourceBucket {
		return nil, nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"connector s3_inventory: the report lists bucket %q, not %q",
			m.SourceBucket,
			sourceBucket,
		))
	}
	cols := schema{}
	for i, name := range strings.Split(m.FileSchema, ",") {
		cols[strings.TrimSpace(name)] = i
	}
	for _, name := range [...]string{colBucket, colKey, colETag, colSize} {
		if _, ok := cols[name]; !ok {
			return nil, nil, apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
				"connector s3_inventory: report has no %s column; add it to the "+
					"inventory configuration's optional fields",
				name,
			))
		}
	}
	return &m, cols, nil
}

// field returns the row's value for column name, or "" when the report lacks it.
func (s schema) field(row []string, name string) string {
	i, ok := s[name]
	if !ok || i >= len(row) {
		return ""
	}
	return row[i]
}

// object maps one report row to the StorageObject a bucket listing returns for the same
// object: the key URL-decoded, the ETag quoted, the time in RFC 3339. ok is false for a
// row the listing would not return: another bucket, a key outside prefix, a non-current
// version or a delete marker.
func (s schema) object(
	row []string,
	bucket, prefix string,
) (interfaces.StorageObject, bool, error) {
	if s.field(row, colBucket) != bucket ||
		s.field(row, colIsLatest) == "false" ||
		s.field(row, colIsDeleteMarker) == "true" {
		return interfaces.StorageObject{}, false, nil
	}
	key, err := url.QueryUnescape(s.field(row, colKey))
	if err != nil {
		return interfaces.StorageObject{}, false, apperr.Wrap(
			err, apperr.CodeInvalidInput, "connector s3_inventory: undecodable key",
		)
	}
	if !strings.HasPrefix(key, prefix) {
		return interfaces.StorageObject{}, false, nil
	}
	obj := interfaces.StorageObject{
		Key:          key,
		LastModified: rfc3339(s.field(row, colLastModified)),
	}
	if etag := s.field(row, colETag); etag != "" {
		obj.ETag = `"` + etag + `"`
	}
	if size := s.field(row, colSize); size != "" {
		if _, err := fmt.Sscan(size, &obj.Size); err != nil {
			return interfaces.StorageObject{}, false, apperr.Wrap(
				err, apperr.CodeInvalidInput, "connector s3_inventory: bad size",
			)
		}
	}
	return obj, true, nil
}

// rfc3339 reformats the report's ISO 8601 time (with milliseconds) the way a listing
// formats LastModified; a value that does not parse is kept as is.
func rfc3339(value string) string {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return t.UTC().Format(time.RFC3339)
}

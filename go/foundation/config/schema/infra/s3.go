package infra

import (
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// S3Config holds S3-compatible storage configuration, including the backend
// selection (AWS S3 vs MinIO) used by the storage client's Kind-factory.
type S3Config struct {
	// Kind selects the storage backend: "s3" (AWS S3 via the default credential
	// chain / IRSA, virtual-host addressing) or "minio" (local MinIO via static
	// credentials and path-style addressing).
	Kind string `mapstructure:"kind" envalias:"S3_KIND"`

	// Endpoint is the S3-compatible API endpoint URL (e.g. "http://localhost:9000"
	// for MinIO). Ignored for Kind "s3" (the SDK resolves the regional endpoint).
	Endpoint string `mapstructure:"endpoint" envalias:"S3_ENDPOINT"`

	// PublicEndpoint is the browser-reachable endpoint used ONLY for presigned URLs.
	// In Docker/K8s the in-cluster Endpoint (e.g. minio:9000) is unreachable from the
	// user's browser; presigned PUT/GET URLs must instead point at a host address.
	// Empty ⇒ falls back to Endpoint (correct for AWS S3, where the SDK endpoint is
	// already public, and for bare-metal MinIO where Endpoint is already localhost).
	PublicEndpoint string `mapstructure:"public_endpoint" envalias:"S3_PUBLIC_ENDPOINT"`

	// Bucket is the name of the service's default bucket.
	Bucket string `mapstructure:"bucket" envalias:"S3_BUCKET"`

	// Region is the AWS region where the bucket is located (e.g. "us-east-1").
	Region string `mapstructure:"region" envalias:"S3_REGION"`

	// AccessKeyID is the static access key for MinIO/dev. Empty for Kind "s3",
	// where credentials come from the default chain (IRSA, env, shared file).
	AccessKeyID string `mapstructure:"access_key_id" envalias:"S3_ACCESS_KEY_ID"`

	// SecretAccessKey is the static secret key for MinIO/dev. Empty for Kind "s3".
	SecretAccessKey string `mapstructure:"secret_access_key" envalias:"S3_SECRET_ACCESS_KEY"`

	// ForcePathStyle uses path-style addressing (bucket in the URL path) instead
	// of virtual-host addressing. Required for MinIO; false for AWS S3.
	ForcePathStyle bool `mapstructure:"force_path_style" envalias:"S3_FORCE_PATH_STYLE"`

	// PresignExpiry is the default lifetime of generated presigned URLs.
	PresignExpiry time.Duration `mapstructure:"presign_expiry"`

	// MultipartPresignExpiry is the lifetime of presigned multipart part-upload URLs.
	// It is longer than PresignExpiry because a large multipart upload streams many
	// parts over a slow link; the stuck-upload reaper's TTL and the orphan/multipart
	// cleanup grace MUST exceed this so a live upload is never reaped mid-flight.
	MultipartPresignExpiry time.Duration `mapstructure:"multipart_presign_expiry"`
}

// EffectiveMultipartPresignExpiry returns the multipart part-URL lifetime, falling
// back to 4× PresignExpiry when unset so a large upload gets a generous window even
// without explicit configuration.
func (c S3Config) EffectiveMultipartPresignExpiry() time.Duration {
	if c.MultipartPresignExpiry > 0 {
		return c.MultipartPresignExpiry
	}
	return 4 * c.PresignExpiry
}

// DefaultS3Config returns an S3Config with defaults for local MinIO. Bucket is left
// empty — the consumer supplies it — so the defaults alone fail Validate until a
// bucket is set.
func DefaultS3Config() S3Config {
	return S3Config{
		Kind:                   "minio",
		Endpoint:               "http://localhost:9000",
		Region:                 "us-east-1",
		AccessKeyID:            "minioadmin",
		SecretAccessKey:        "minioadmin",
		ForcePathStyle:         true,
		PresignExpiry:          15 * time.Minute,
		MultipartPresignExpiry: 60 * time.Minute,
	}
}

// Validate returns an error if the configuration is invalid: the Kind must be a
// known backend ("s3" or "minio") and the Bucket must be set.
func (c S3Config) Validate() error {
	switch c.Kind {
	case "s3", "minio":
	default:
		return apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("invalid s3 kind %q: must be \"s3\" or \"minio\"", c.Kind),
		)
	}
	if c.Bucket == "" {
		return apperr.InvalidInput("s3 bucket must not be empty")
	}
	// A multipart part-URL must live at least as long as a single-part PUT URL:
	// EffectiveMultipartPresignExpiry is what the reaper TTL and cleanup grace are
	// sized above, so a shorter value would let a live multipart upload be reaped
	// mid-flight (the exact race MultipartPresignExpiry's doc warns about).
	if c.MultipartPresignExpiry != 0 && c.MultipartPresignExpiry < c.PresignExpiry {
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"s3 multipart_presign_expiry (%s) must be >= presign_expiry (%s)",
			c.MultipartPresignExpiry, c.PresignExpiry,
		))
	}
	return nil
}

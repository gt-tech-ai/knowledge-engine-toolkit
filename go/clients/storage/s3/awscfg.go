// Package s3 implements the StorageClient against S3-compatible object stores
// (AWS S3 and MinIO), selected by configuration. It is the backend subpackage of
// clients/storage; the storage tier root selects it by Kind.
package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
)

// kindMinIO is the infra.S3Config Kind of the MinIO (local/dev) backend, which uses
// path-style addressing, an explicit endpoint and static credentials.
const kindMinIO = "minio"

// s3OptionsApplier returns the s3.Options mutator encoding the dev/stage backend
// differences: MinIO needs path-style addressing and an explicit endpoint, while
// AWS S3 uses virtual-host addressing and the SDK-resolved regional endpoint.
func s3OptionsApplier(cfg infra.S3Config) func(*s3.Options) {
	return func(o *s3.Options) {
		o.UsePathStyle = cfg.ForcePathStyle
		if cfg.Kind == kindMinIO && cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	}
}

// UseStaticCredentials reports whether the client should present static
// credentials from config. Only the MinIO/dev backend (kind="minio") does; the
// AWS backend (kind="s3") ALWAYS uses the default credential chain (IRSA in
// cluster). Gating on Kind — not merely on AccessKeyID being set — is essential:
// base.yaml defaults access_key_id to "minioadmin", and staging/prod do not blank
// the storage.s3 creds (unlike messaging.sqs), so an s3 config carries that stray
// "minioadmin" key. Presenting it to real AWS S3 fails every request with
// InvalidAccessKeyId and bypasses IRSA — the staging orphaned-file-cleanup /
// presign / download breakage this guards against.
func UseStaticCredentials(cfg infra.S3Config) bool {
	return cfg.Kind == kindMinIO && cfg.AccessKeyID != ""
}

// NewAWSClient builds an aws-sdk-go-v2 S3 client for the given config. Static
// credentials are used only for the MinIO/dev backend; the AWS backend (kind="s3")
// uses the default credential chain (IRSA / env / shared-file). See
// UseStaticCredentials.
func NewAWSClient(ctx context.Context, cfg infra.S3Config) (*s3.Client, error) {
	// Retry with exponential backoff on transient errors (5xx, throttling) is
	// provided by the SDK's standard retryer — this is the correct layer for it,
	// since the StorageClient decorator cannot safely retry body-consuming uploads.
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithRetryMode(aws.RetryModeStandard),
		awsconfig.WithRetryMaxAttempts(4),
	}
	if UseStaticCredentials(cfg) {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AccessKeyID,
				cfg.SecretAccessKey,
				"",
			),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "loading aws config")
	}
	return s3.NewFromConfig(awsCfg, s3OptionsApplier(cfg)), nil
}

// NewAWSClientWithCredentials builds an S3 client that signs with an EXPLICIT credentials
// provider and region, instead of NewAWSClient's app-level MinIO-static / AWS-IRSA
// selection. It is the connector-scoped path: a connector presents its OWN credentials
// (STS-assumed role or a static access key) to reach an external bucket.
// Path-style/endpoint handling (MinIO vs AWS) still comes from cfg via s3OptionsApplier.
func NewAWSClientWithCredentials(
	ctx context.Context,
	cfg infra.S3Config,
	creds aws.CredentialsProvider,
	region string,
) (*s3.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithRetryMode(aws.RetryModeStandard),
		awsconfig.WithRetryMaxAttempts(4),
		awsconfig.WithCredentialsProvider(creds),
	)
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"loading aws config with credentials",
		)
	}
	return s3.NewFromConfig(awsCfg, s3OptionsApplier(cfg)), nil
}

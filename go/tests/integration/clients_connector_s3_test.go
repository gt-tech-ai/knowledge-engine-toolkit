//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	connectorpkg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector"
	connectors3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3"
	storages3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/s3"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	miniofix "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/minio"
)

// minioS3Client builds a connector-scoped StorageClient against the MinIO endpoint — the
// test's S3ClientBuilder, mirroring the app composition root's closure (the seam that
// keeps go/clients/connector free of a lateral storage import).
func minioS3Client(endpoint string) connectors3.S3ClientBuilder {
	return func(
		ctx context.Context,
		creds aws.CredentialsProvider,
		region string,
	) (interfaces.StorageClient, error) {
		client, err := storages3.NewAWSClientWithCredentials(ctx, infra.S3Config{
			Kind:           "minio",
			Endpoint:       endpoint,
			ForcePathStyle: true,
			Region:         region,
		}, creds, region)
		if err != nil {
			return nil, err
		}
		return storages3.NewClient(client, "", ""), nil
	}
}

// TestConnectorS3Source_TestConnection_RealMinIO drives the pkg S3 ConnectorSource
// end-to-end against a real MinIO container (via the family SourceBuilder + a
// connector-scoped, credential-bearing StorageClient): it counts exactly the objects
// under the prefix, and classifies a missing bucket.
//
// Why this test is important:
//   - TestConnection's whole value is a REAL reachability + credential check; only a live
//     S3-compatible endpoint proves the connector-scoped client (built from the
//     access-key strategy + the injected builder) authenticates, lists a bounded page,
//     and counts — and that a bad bucket is surfaced as a truthful, correctly-coded
//     error, not a cryptic crypto/transport failure.
//
// What it tests:
//   - the SourceBuilder (s3 kind, access-key auth) builds a source whose TestConnection
//     returns the number of objects under bucket/prefix; a nonexistent bucket yields
//     CodeInvalidInput.
func TestConnectorS3Source_TestConnection_RealMinIO(t *testing.T) {
	ctx := context.Background()
	m, err := miniofix.NewTestMinIO(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { m.Close(ctx) })

	const bucket = "corp-connector"
	adminCreds := credentials.NewStaticCredentialsProvider(
		miniofix.RootUser,
		miniofix.RootPassword,
		"",
	)
	admin, err := storages3.NewAWSClientWithCredentials(ctx, infra.S3Config{
		Kind:           "minio",
		Endpoint:       m.Endpoint(),
		ForcePathStyle: true,
		Region:         "us-east-1",
	}, adminCreds, "us-east-1")
	require.NoError(t, err)

	_, err = admin.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	// Two objects under docs/, one outside the prefix (must not be counted).
	for _, key := range []string{"docs/a.txt", "docs/b.txt", "other/c.txt"} {
		_, err = admin.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
			Body:   strings.NewReader("content"),
		})
		require.NoError(t, err)
	}

	// No STS: this test uses the access-key strategy.
	builder := connectorpkg.NewSourceBuilder(connectorpkg.Config{}, connectorpkg.Deps{
		BuildS3Client: minioS3Client(m.Endpoint()),
	})
	cfg := interfaces.SourceConfig{
		Kind:            interfaces.ConnectorKindS3,
		Bucket:          bucket,
		Prefix:          "docs/",
		Region:          "us-east-1",
		AuthMethod:      "access_key",
		AccessKeyID:     miniofix.RootUser,
		SecretAccessKey: miniofix.RootPassword,
	}

	src, err := builder.Build(ctx, cfg)
	require.NoError(t, err)
	n, err := src.TestConnection(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "only the two docs/ objects are under the prefix")

	// A nonexistent bucket is a client-fault error → CodeInvalidInput (not a transport
	// error).
	badCfg := cfg
	badCfg.Bucket = "does-not-exist"
	badCfg.Prefix = ""
	badSrc, err := builder.Build(ctx, badCfg)
	require.NoError(t, err)
	_, err = badSrc.TestConnection(ctx)
	require.True(
		t,
		apperr.Is(err, apperr.CodeInvalidInput),
		"a missing bucket is InvalidInput",
	)
}

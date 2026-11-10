//go:build integration

package integration_test

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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	miniofix "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/minio"
)

// minioS3Client builds a connector-scoped StorageClient against the MinIO endpoint — the
// test's S3ClientBuilder, mirroring the app composition root's closure (the seam that
// keeps go/clients/connector free of a lateral storage import).
func minioS3Client(endpoint string) connectors3.ClientBuilder {
	return func(
		ctx context.Context,
		creds aws.CredentialsProvider,
		region string,
	) (interfaces.StorageClient, error) {
		client, err := storages3.NewAWSClientWithCredentials(
			ctx,
			fixtures.BuildRef(func(row *infra.S3Config) {
				row.Kind = textMinio
				row.Endpoint = endpoint
				row.ForcePathStyle = true
				row.Region = region
			}),
			creds,
			region,
		)
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
	t.Parallel()

	ctx := context.Background()
	testMinIO, err := miniofix.NewTestMinIO(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { testMinIO.Close(ctx) })

	testConnectorS3SourceTestConnectionRealMinIOSeedsAndConnects(ctx, t, testMinIO)
}

// minioAccessKeySource returns an S3 source over docs/ in bucket that signs in with the
// MinIO root access key.
func minioAccessKeySource(bucket string) interfaces.SourceConfig {
	return fixtures.Build(func(row *interfaces.SourceConfig) {
		row.Kind = interfaces.ConnectorKindS3
		row.Bucket = bucket
		row.Prefix = "docs/"
		row.Region = textUsEast1
		row.AuthMethod = "access_key"
		row.AccessKeyID = miniofix.RootUser
		row.SecretAccessKey = miniofix.RootPassword
	})
}

// testConnectorS3SourceTestConnectionRealMinIOSeedsAndConnects continues
// TestConnectorS3Source_TestConnection_RealMinIO.
func testConnectorS3SourceTestConnectionRealMinIOSeedsAndConnects(ctx context.Context,
	t *testing.T,
	testMinIO *miniofix.TestMinIO,
) {
	t.Helper()

	const bucket = "corp-connector"

	adminCreds := credentials.NewStaticCredentialsProvider(
		miniofix.RootUser,
		miniofix.RootPassword,
		"",
	)
	admin, err := storages3.NewAWSClientWithCredentials(
		ctx,
		fixtures.BuildRef(func(row *infra.S3Config) {
			row.Kind = textMinio
			row.Endpoint = testMinIO.Endpoint()
			row.ForcePathStyle = true
			row.Region = textUsEast1
		}),
		adminCreds,
		textUsEast1,
	)
	require.NoError(t, err)

	_, err = admin.CreateBucket(
		ctx,
		fixtures.BuildRef(func(row *awss3.CreateBucketInput) {
			row.Bucket = aws.String(bucket)
		}),
	)
	require.NoError(t, err)

	// Two objects under docs/, one outside the prefix (must not be counted).
	for _, key := range []string{"docs/a.txt", "docs/b.txt", "other/c.txt"} {
		_, err = admin.PutObject(ctx, fixtures.BuildRef(func(row *awss3.PutObjectInput) {
			row.Bucket = aws.String(bucket)
			row.Key = aws.String(key)
			row.Body = strings.NewReader("content")
		}))
		require.NoError(t, err)
	}

	// No STS: this test uses the access-key strategy.
	builder := connectorpkg.NewSourceBuilder(
		stdx.Zero[connectorpkg.Config](),
		connectorpkg.Deps{
			BuildS3Client: minioS3Client(testMinIO.Endpoint()),
			Decorations:   decorate.Decorations{},
			STS:           nil,
		},
	)
	cfg := minioAccessKeySource(bucket)

	testConnectorS3SourceTestConnectionRealMinIOListsOnlyDocsObjects(
		ctx,
		t,
		builder,
		&cfg,
	)
}

// testConnectorS3SourceTestConnectionRealMinIOListsOnlyDocsObjects continues
// TestConnectorS3Source_TestConnection_RealMinIO: lists only docs objects.
func testConnectorS3SourceTestConnectionRealMinIOListsOnlyDocsObjects(
	ctx context.Context,
	t *testing.T,
	builder interfaces.SourceBuilder,
	cfg *interfaces.SourceConfig,
) {
	t.Helper()

	var err error

	src, err := builder.Build(ctx, cfg)
	require.NoError(t, err)

	n, err := src.TestConnection(ctx)
	require.NoError(t, err)
	assert.Equal(t, wantN, n, "only the two docs/ objects are under the prefix")

	// A nonexistent bucket is a client-fault error → CodeInvalidInput (not a transport
	// error).
	badCfg := *cfg

	badCfg.Bucket = "does-not-exist"
	badCfg.Prefix = ""

	badSrc, err := builder.Build(ctx, &badCfg)
	require.NoError(t, err)

	_, err = badSrc.TestConnection(ctx)
	require.True(
		t,
		apperr.Is(err, apperr.CodeInvalidInput),
		"a missing bucket is InvalidInput",
	)
}

const (
	// wantN is the expected n.
	wantN = 2
)

//go:build integration

package integration_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	connectorpkg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector"
	storages3 "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage/s3"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	miniofix "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/minio"
)

// writeInventory writes an S3 Inventory report of bucket (CSV, gzipped, split into data
// files of perFile rows) from a real listing, the way S3 writes one: unquoted ETags,
// URL-encoded keys, ISO 8601 times. It returns the manifest key.
func writeInventory(
	ctx context.Context,
	t *testing.T,
	admin *awss3.Client,
	bucket, reportBucket string,
	perFile int,
) string {
	t.Helper()

	return writeInventoryRest5(ctx, t, admin, bucket, reportBucket, perFile)
}

// writeInventoryRest5 continues writeInventoryRest4 with its remaining steps.
func writeInventoryRest5(
	ctx context.Context,
	t *testing.T,
	admin *awss3.Client,
	bucket, reportBucket string,
	perFile int,
) string {
	t.Helper()

	listed, err := admin.ListObjectsV2(
		ctx,
		&awss3.ListObjectsV2Input{Bucket: aws.String(bucket)},
	)
	require.NoError(t, err)

	var files []map[string]any

	for start := 0; start < len(listed.Contents); start += perFile {
		body := inventoryCSV(t, bucket, listed, start, perFile)

		key := fmt.Sprintf("inv/%s/data/%d.csv.gz", bucket, len(files))

		_, err = admin.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(
				reportBucket,
			),
			Key:  aws.String(key),
			Body: bytes.NewReader(body),
		})
		require.NoError(t, err)

		files = append(files, map[string]any{"key": key})
	}

	manifest, err := json.Marshal(map[string]any{
		"sourceBucket": bucket,
		"fileFormat":   "CSV",
		"fileSchema":   "Bucket, Key, Size, LastModifiedDate, ETag",
		"files":        files,
	})
	require.NoError(t, err)

	manifestKey := "inv/" + bucket + "/manifest.json"

	_, err = admin.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(
			reportBucket,
		),
		Key:  aws.String(manifestKey),
		Body: bytes.NewReader(manifest),
	})
	require.NoError(t, err)

	return manifestKey
}

// crawl pages src to the end with limit, returning every object.
func crawl(
	ctx context.Context,
	t *testing.T,
	src interfaces.ConnectorSource,
	limit int,
) []interfaces.StorageObject {
	t.Helper()

	var all []interfaces.StorageObject

	token := ""

	for {
		page, next, err := src.ListPage(ctx, token, limit)
		require.NoError(t, err)

		all = append(all, page...)
		if next == "" {
			return all
		}

		token = next
	}
}

// TestS3Inventory_ListsAWrittenReportLikeTheBucket tests the s3_inventory source end to
// end against a real MinIO: a report written from the bucket lists the same objects as
// the s3 kind's crawl.
//
// Why this test is important:
//   - Only a real object store proves the report is downloaded, gunzipped and decoded
//     across data files with the connector's credentials, and that its objects (keys with
//     spaces and parentheses, ETags, times) are exactly what the bucket listing yields.
//
// What it tests:
//   - seven objects (five under docs/, one with a space and parentheses in its key) and a
//     three-row-per-file report in a separate bucket: the inventory crawl in pages of two
//     equals the s3 kind's crawl of docs/
func TestS3Inventory_ListsAWrittenReportLikeTheBucket(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, err := miniofix.NewTestMinIO(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { m.Close(ctx) })

	admin, err := storages3.NewAWSClientWithCredentials(ctx, &infra.S3Config{
		Kind:           textMinio,
		Endpoint:       m.Endpoint(),
		ForcePathStyle: true,
		Region:         textUsEast1,
	}, credentials.NewStaticCredentialsProvider(
		miniofix.RootUser, miniofix.RootPassword, "",
	), textUsEast1)
	require.NoError(t, err)

	testS3InventoryListsAWrittenReportLikeTheBucketRest(ctx, t, m, admin)
}

// testS3InventoryListsAWrittenReportLikeTheBucketRest continues
// TestS3Inventory_ListsAWrittenReportLikeTheBucket with its remaining steps.
func testS3InventoryListsAWrittenReportLikeTheBucketRest(
	ctx context.Context, t *testing.T, m *miniofix.TestMinIO, admin *awss3.Client,
) {
	t.Helper()

	var err error

	const bucket, reports = "corp-docs", "corp-reports"

	for _, b := range []string{bucket, reports} {
		_, err = admin.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(b)})
		require.NoError(t, err)
	}

	keys := []string{
		"docs/a.pdf", "docs/b.pdf", "docs/my report (2).pdf", "docs/d.pdf", "docs/e.pdf",
		"other/x.pdf", "other/y.pdf",
	}
	for i, key := range keys {
		_, err = admin.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(
				bucket,
			),
			Key:  aws.String(key),
			Body: strings.NewReader(strings.Repeat("x", i+1)),
		})
		require.NoError(t, err)
	}

	manifestKey := writeInventory(ctx, t, admin, bucket, reports, perFile3)
	builder := connectorpkg.NewSourceBuilder(connectorpkg.Config{}, connectorpkg.Deps{
		BuildS3Client: minioS3Client(m.Endpoint()),
	})
	cfg := interfaces.SourceConfig{
		Kind:            interfaces.ConnectorKindS3,
		Bucket:          bucket,
		Prefix:          "docs/",
		Region:          textUsEast1,
		AuthMethod:      "access_key",
		AccessKeyID:     miniofix.RootUser,
		SecretAccessKey: miniofix.RootPassword,
	}
	listing, err := builder.Build(ctx, &cfg)
	require.NoError(t, err)

	cfg.Kind = interfaces.ConnectorKindS3Inventory
	cfg.InventoryBucket = reports
	cfg.InventoryManifestKey = manifestKey

	inventory, err := builder.Build(ctx, &cfg)
	require.NoError(t, err)

	want := crawl(ctx, t, listing, limit1000)
	got := crawl(ctx, t, inventory, limit2)

	require.Len(t, want, wantWantLen5)
	require.Equal(t, want, got)
}

// inventoryCSV renders one gzipped report file from up to perFile listed objects.
func inventoryCSV(
	t *testing.T,
	bucket string,
	listed *awss3.ListObjectsV2Output,
	start, perFile int,
) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	rows := csv.NewWriter(gz)

	for _, o := range listed.Contents[start:min(start+perFile, len(listed.Contents))] {
		require.NoError(t, rows.Write([]string{
			bucket,
			url.QueryEscape(aws.ToString(o.Key)),
			strconv.FormatInt(aws.ToInt64(o.Size), 10),
			o.LastModified.UTC().Format("2006-01-02T15:04:05.000Z"),
			strings.Trim(aws.ToString(o.ETag), `"`),
		}))
	}

	rows.Flush()
	require.NoError(t, rows.Error())
	require.NoError(t, gz.Close())

	return buf.Bytes()
}

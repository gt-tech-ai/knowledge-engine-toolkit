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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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

	return writeInventoryReportFiles(ctx, t, admin, bucket, reportBucket, perFile)
}

// writeInventoryReportFiles continues writeInventory: report files.
func writeInventoryReportFiles(
	ctx context.Context,
	t *testing.T,
	admin *awss3.Client,
	bucket, reportBucket string,
	perFile int,
) string {
	t.Helper()

	listed, err := admin.ListObjectsV2(
		ctx,
		fixtures.BuildRef(func(row *awss3.ListObjectsV2Input) {
			row.Bucket = aws.String(bucket)
		}),
	)
	require.NoError(t, err)

	var files []map[string]any

	for start := 0; start < len(listed.Contents); start += perFile {
		body := inventoryCSV(t, bucket, listed, start, perFile)

		key := fmt.Sprintf("inv/%s/data/%d.csv.gz", bucket, len(files))

		_, err = admin.PutObject(ctx, fixtures.BuildRef(func(row *awss3.PutObjectInput) {
			row.Bucket = aws.String(
				reportBucket,
			)
			row.Key = aws.String(key)
			row.Body = bytes.NewReader(body)
		}))
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

	_, err = admin.PutObject(ctx, fixtures.BuildRef(func(row *awss3.PutObjectInput) {
		row.Bucket = aws.String(
			reportBucket,
		)
		row.Key = aws.String(manifestKey)
		row.Body = bytes.NewReader(manifest)
	}))
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
	testMinIO, err := miniofix.NewTestMinIO(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { testMinIO.Close(ctx) })

	admin, err := storages3.NewAWSClientWithCredentials(
		ctx,
		fixtures.BuildRef(func(row *infra.S3Config) {
			row.Kind = textMinio
			row.Endpoint = testMinIO.Endpoint()
			row.ForcePathStyle = true
			row.Region = textUsEast1
		}),
		credentials.NewStaticCredentialsProvider(
			miniofix.RootUser, miniofix.RootPassword, "",
		),
		textUsEast1,
	)
	require.NoError(t, err)

	testS3InventoryListsAWrittenReportLikeTheBucketComparesListings(
		ctx,
		t,
		testMinIO,
		admin,
	)
}

// testS3InventoryListsAWrittenReportLikeTheBucketComparesListings continues
// TestS3Inventory_ListsAWrittenReportLikeTheBucket: compares listings.
func testS3InventoryListsAWrittenReportLikeTheBucketComparesListings(
	ctx context.Context, t *testing.T, testMinIO *miniofix.TestMinIO, admin *awss3.Client,
) {
	t.Helper()

	testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsPrepare(ctx,
		t,
		testMinIO,
		admin,
	)
}

// testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsPrepare continues
// testS3InventoryListsAWrittenReportLikeTheBucketComparesListings.
func testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsPrepare(
	ctx context.Context,
	t *testing.T,
	testMinIO *miniofix.TestMinIO,
	admin *awss3.Client,
) {
	t.Helper()

	testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsUpload(ctx,
		t,
		testMinIO,
		admin,
	)
}

// testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsUpload continues
// testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsPrepare.
func testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsUpload(
	ctx context.Context,
	t *testing.T,
	testMinIO *miniofix.TestMinIO,
	admin *awss3.Client,
) {
	t.Helper()

	testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsKeys(ctx,
		t,
		testMinIO,
		admin,
	)
}

// testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsKeys continues
// testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsUpload.
func testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsKeys(
	ctx context.Context,
	t *testing.T,
	testMinIO *miniofix.TestMinIO,
	admin *awss3.Client,
) {
	t.Helper()

	const bucket, reports = "corp-docs", "corp-reports"

	for _, b := range []string{bucket, reports} {
		_, err := admin.CreateBucket(
			ctx,
			fixtures.BuildRef(func(row *awss3.CreateBucketInput) {
				row.Bucket = aws.String(b)
			}),
		)
		require.NoError(t, err)
	}

	keys := []string{
		"docs/a.pdf", "docs/b.pdf", "docs/my report (2).pdf", "docs/d.pdf", "docs/e.pdf",
		"other/x.pdf", "other/y.pdf",
	}
	for index, key := range keys {
		_, err := admin.PutObject(ctx, fixtures.BuildRef(func(row *awss3.PutObjectInput) {
			row.Bucket = aws.String(bucket)
			row.Key = aws.String(key)
			row.Body = strings.NewReader(strings.Repeat("x", index+1))
		}))
		require.NoError(t, err)
	}

	manifestKey := writeInventory(ctx, t, admin, bucket, reports, writeInventoryInput)
	builder := connectorpkg.NewSourceBuilder(
		stdx.Zero[connectorpkg.Config](),
		connectorpkg.Deps{
			BuildS3Client: minioS3Client(testMinIO.Endpoint()),
			Decorations:   decorate.Decorations{},
			STS:           nil,
			GitHub:        nil,
			Git:           nil,
		},
	)
	cfg := minioAccessKeySource(bucket)
	listing, err := builder.Build(ctx, &cfg)
	require.NoError(t, err)

	cfg.Kind = interfaces.ConnectorKindS3Inventory
	cfg.InventoryBucket = reports
	cfg.InventoryManifestKey = manifestKey

	inventory, err := builder.Build(ctx, &cfg)
	require.NoError(t, err)

	want := crawl(ctx, t, listing, crawlInput)
	got := crawl(ctx, t, inventory, crawlInputListings)

	require.Len(t, want, wantWantLen)
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

	writer := gzip.NewWriter(&buf)
	rows := csv.NewWriter(writer)

	for _, object := range listed.Contents[start:min(start+perFile, len(listed.Contents))] {
		require.NoError(t, rows.Write([]string{
			bucket,
			url.QueryEscape(aws.ToString(object.Key)),
			strconv.FormatInt(aws.ToInt64(object.Size), 10),
			object.LastModified.UTC().Format("2006-01-02T15:04:05.000Z"),
			strings.Trim(aws.ToString(object.ETag), `"`),
		}))
	}

	rows.Flush()
	require.NoError(t, rows.Error())
	require.NoError(t, writer.Close())

	return buf.Bytes()
}

const (
	// crawlInput is the limit passed to crawl in
	// testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsKeys.
	crawlInput = 1000

	// crawlInputListings is the limit passed to crawl in
	// testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsKeys.
	crawlInputListings = 2

	// wantWantLen is the expected want len.
	wantWantLen = 5

	// writeInventoryInput is the perFile passed to writeInventory in
	// testS3InventoryListsAWrittenReportLikeTheBucketComparesListingsKeys.
	writeInventoryInput = 3
)

//go:build integration

// This file verifies the S3 StorageClient against a real MinIO instance (via
// testcontainers). Unit tests in go/tests/unit cover error mapping, the multipart
// decision, and the decorator stack with mocks; this suite targets end-to-end
// correctness over a real object store, driven by committed fixtures under
// testdata/.
package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/storage"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/infra"
	miniodb "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/dbtest/minio"
	testsuite "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite"
)

const testBucket = "test-documents"

// StorageMinIOSuite runs the StorageClient against a real MinIO container.
type StorageMinIOSuite struct {
	// client is the StorageClient under test, pointed at the test container.
	client interfaces.StorageClient
	testsuite.MinIOIntegrationSuite
}

// TestStorageMinIOSuite is the testify entrypoint for the MinIO integration suite.
//
// Why this test is important:
//   - Without a top-level TestXxx, go test ignores every suite method
//
// What it tests:
//   - Wires StorageMinIOSuite into the runner
func TestStorageMinIOSuite(t *testing.T) {
	suite.Run(t, new(StorageMinIOSuite))
}

// TestNewTestMinIO_WithImageOverridesTheDefault tests that the MinIO fixture starts
// the image a consumer passes instead of its pinned default.
//
// Why this test is important:
//   - The pinned image will go stale or vanish (the old quay.io pin did); consumers
//     must be able to repin in their own tests without an engine release.
//
// What it tests:
//   - WithImage with another reference to the pinned image (digest only, no tag)
//     starts a reachable MinIO.
//   - WithImage with a tag that does not exist fails to start and names that image —
//     a start that could only fail if the override replaced DefaultImage.
//   - It needs a Docker daemon: without one both cases would fail or pass for the
//     wrong reason, so it skips.
func TestNewTestMinIO_WithImageOverridesTheDefault(t *testing.T) {
	if testing.Short() || (os.Getenv("INTEGRATION") == "" && os.Getenv("CI") == "") {
		t.Skip("set INTEGRATION=1 or CI=1 to run integration tests")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	digest := strings.SplitN(miniodb.DefaultImage, "@", 2)[1]
	digestOnly := "cgr.dev/chainguard/minio@" + digest
	started, err := miniodb.NewTestMinIO(ctx, miniodb.WithImage(digestOnly))
	t.Cleanup(func() { started.Close(ctx) })
	require.NoError(t, err, "the overriding reference starts")
	//nolint:noctx // test probe
	resp, err := http.Get(started.Endpoint() + "/minio/health/ready")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	const missing = "cgr.dev/chainguard/minio:ke-fixture-override-probe-does-not-exist"
	m, err := miniodb.NewTestMinIO(ctx, miniodb.WithImage(missing))
	t.Cleanup(func() { m.Close(ctx) })
	require.Error(t, err)
	require.ErrorContains(t, err, missing)
}

func (s *StorageMinIOSuite) SetupSuite() {
	s.MinIOIntegrationSuite.SetupSuite()

	// Create the bucket with a raw client before building the StorageClient.
	_, err := s.rawClient().CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(testBucket),
	})
	s.Require().NoError(err, "create bucket")

	cfg := infra.DefaultS3Config()
	cfg.Endpoint = s.MinIOEndpoint
	cfg.Bucket = testBucket
	c, err := storage.NewFromConfig(context.Background(), storage.KindS3, cfg)
	s.Require().NoError(err, "build storage client")
	s.client = c
}

// rawClient builds a path-style S3 client pointed at the test MinIO instance.
func (s *StorageMinIOSuite) rawClient() *awss3.Client {
	awsCfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				miniodb.RootUser,
				miniodb.RootPassword,
				"",
			),
		),
	)
	s.Require().NoError(err)
	return awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(s.MinIOEndpoint)
	})
}

// loadFixture reads the committed sample.md document fixture from testdata/.
func (s *StorageMinIOSuite) loadFixture() []byte {
	data, err := os.ReadFile(filepath.Join("testdata", "sample.md"))
	s.Require().NoError(err, "read fixture sample.md")
	return data
}

// largeFixture returns a deterministic payload of at least minBytes by repeating
// the sample fixture — exercises the multipart path without committing a large
// binary or relying on randomness.
func (s *StorageMinIOSuite) largeFixture(minBytes int) []byte {
	base := s.loadFixture()
	return bytes.Repeat(base, (minBytes/len(base))+1)
}

// TestUploadDownload_SmallAndMultipart verifies the round-trip for both the
// single-part and multipart paths using fixture content.
//
// Why this test is important:
//   - Multipart is selected automatically for large bodies; a broken assembly
//     corrupts large documents silently
//
// What it tests:
//   - A small fixture round-trips byte-identically (single-part)
//   - A >5 MiB fixture-derived payload round-trips byte-identically (multipart)
func (s *StorageMinIOSuite) TestUploadDownload_SmallAndMultipart() {
	ctx := context.Background()

	small := s.loadFixture()
	s.Require().
		NoError(
			s.client.Upload(ctx, testBucket, "small.md", bytes.NewReader(small), "text/markdown"),
		)
	s.Equal(small, s.download(ctx, "small.md"))

	large := s.largeFixture(6 * 1024 * 1024)
	s.Require().
		NoError(
			s.client.Upload(ctx, testBucket, "large.md", bytes.NewReader(large), "text/markdown"),
		)
	got := s.download(ctx, "large.md")
	s.Require().Len(got, len(large))
	s.True(bytes.Equal(large, got), "multipart round-trip must be byte-identical")
}

// TestPresignedURL_PutGet verifies a presigned PUT upload followed by a presigned
// GET download works against MinIO.
//
// Why this test is important:
//   - Direct browser-to-storage upload depends on presigned PUT URLs
//     actually being accepted by the backend
//
// What it tests:
//   - A presigned PUT stores the object; a presigned GET returns the same bytes
func (s *StorageMinIOSuite) TestPresignedURL_PutGet() {
	ctx := context.Background()
	payload := s.loadFixture()
	const ct = "text/markdown"

	putURL, err := s.client.PresignPutURL(ctx, testBucket, "presigned.md", 300, ct)
	s.Require().NoError(err)

	putReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		putURL,
		bytes.NewReader(payload),
	)
	s.Require().NoError(err)
	putReq.Header.Set("Content-Type", ct)
	putResp, err := http.DefaultClient.Do(putReq)
	s.Require().NoError(err)
	s.Require().NoError(putResp.Body.Close())
	s.Require().Equal(http.StatusOK, putResp.StatusCode)

	getURL, err := s.client.PresignURL(
		ctx,
		testBucket,
		"presigned.md",
		300,
		"My Report.md",
	)
	s.Require().NoError(err)
	getReq, err := http.NewRequestWithContext(
		s.T().Context(), http.MethodGet, getURL, http.NoBody,
	)
	s.Require().NoError(err)
	getResp, err := http.DefaultClient.Do(getReq)
	s.Require().NoError(err)
	defer getResp.Body.Close()
	// The signed response-content-disposition override must round-trip: MinIO returns
	// it verbatim on the GET, forcing an attachment download under the display name.
	s.Contains(getResp.Header.Get("Content-Disposition"), "attachment")
	s.Contains(getResp.Header.Get("Content-Disposition"), "My Report.md")
	body, err := io.ReadAll(getResp.Body)
	s.Require().NoError(err)
	s.Equal(payload, body)
}

// TestStat_And_List verifies metadata reads and prefix listing.
//
// Why this test is important:
//   - Content-type detection (Stat) and prefix listing (connector diffing) are
//     load-bearing for any caller that reads or syncs objects
//
// What it tests:
//   - Stat returns the stored content-type and size
//   - ListObjects returns all objects under a prefix
func (s *StorageMinIOSuite) TestStat_And_List() {
	ctx := context.Background()
	body := s.loadFixture()
	for _, k := range []string{"docs/a.md", "docs/b.md", "other/c.md"} {
		s.Require().
			NoError(s.client.Upload(ctx, testBucket, k, bytes.NewReader(body), "text/markdown"))
	}

	stat, err := s.client.Stat(ctx, testBucket, "docs/a.md")
	s.Require().NoError(err)
	s.Equal("text/markdown", stat.ContentType)
	s.Positive(stat.Size)

	objs, err := s.client.ListObjects(ctx, testBucket, "docs/")
	s.Require().NoError(err)
	s.Len(objs, 2, "only docs/ prefix objects")
}

// TestListObjectsPageToken_PagesToExhaustion verifies the resumable, token-paginated
// crawl over a REAL MinIO listing that spans multiple pages.
//
// Why this test is important:
//   - ListObjectsPageToken is the PR's core new primitive — the memory-bounded streaming
//     the connector sync engine crawls a large bucket with, checkpointing the
//     continuation token to crash-resume. Real continuation-token / IsTruncated behavior
//     (a page returns a non-empty token only while more pages remain, then "" to
//     terminate the loop) is exactly what a mocked S3API can't verify; the sibling
//     ListObjects + TestConnection have real-MinIO coverage but this primitive did not.
//
// What it tests:
//   - Paging a 5-object prefix with limit=2 yields 3 pages (2 + 2 + 1) whose tokens
//     chain, terminates on an empty token, and returns every object exactly once (no
//     gaps, no duplicates).
func (s *StorageMinIOSuite) TestListObjectsPageToken_PagesToExhaustion() {
	ctx := context.Background()
	body := s.loadFixture()
	want := []string{"page/a", "page/b", "page/c", "page/d", "page/e"}
	for _, k := range want {
		s.Require().
			NoError(s.client.Upload(ctx, testBucket, k, bytes.NewReader(body), "text/markdown"))
	}

	var got []string
	token := ""
	pages := 0
	for {
		objs, next, err := s.client.ListObjectsPageToken(
			ctx,
			testBucket,
			"page/",
			token,
			2,
		)
		s.Require().NoError(err)
		pages++
		s.Require().
			Less(pages, 10, "the crawl must terminate (guard against a non-empty-token loop)")
		s.Require().LessOrEqual(len(objs), 2, "each page honors the limit")
		for _, o := range objs {
			got = append(got, o.Key)
		}
		if next == "" {
			break
		}
		token = next
	}

	s.Equal(3, pages, "5 objects at limit=2 => 3 pages (2+2+1)")
	s.ElementsMatch(
		want,
		got,
		"every object is returned exactly once across the paged crawl",
	)
}

// TestDelete_DeleteBatch_Exists verifies single + batch delete and existence checks.
//
// Why this test is important:
// - Orphaned-file cleanup relies on batch delete and Exists semantics
//
// What it tests:
//   - Exists is true after upload, false after delete
//   - DeleteBatch removes multiple keys
func (s *StorageMinIOSuite) TestDelete_DeleteBatch_Exists() {
	ctx := context.Background()
	body := s.loadFixture()
	s.Require().
		NoError(
			s.client.Upload(ctx, testBucket, "del/one.md", bytes.NewReader(body), "text/markdown"),
		)

	exists, err := s.client.Exists(ctx, testBucket, "del/one.md")
	s.Require().NoError(err)
	s.True(exists)

	s.Require().NoError(s.client.Delete(ctx, testBucket, "del/one.md"))
	exists, err = s.client.Exists(ctx, testBucket, "del/one.md")
	s.Require().NoError(err)
	s.False(exists)

	for _, k := range []string{"batch/x.md", "batch/y.md"} {
		s.Require().
			NoError(s.client.Upload(ctx, testBucket, k, bytes.NewReader(body), "text/markdown"))
	}
	s.Require().
		NoError(s.client.DeleteBatch(ctx, testBucket, []string{"batch/x.md", "batch/y.md"}))
	objs, err := s.client.ListObjects(ctx, testBucket, "batch/")
	s.Require().NoError(err)
	s.Empty(objs)
}

// download is a helper that reads an object fully.
func (s *StorageMinIOSuite) download(ctx context.Context, key string) []byte {
	r, err := s.client.Download(ctx, testBucket, key)
	s.Require().NoError(err)
	defer r.Close()
	data, err := io.ReadAll(r)
	s.Require().NoError(err)
	return data
}

package unit_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3inventory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// inventorySchema is the report's column order in these fixtures.
const inventorySchema = "Bucket, Key, Size, LastModifiedDate, ETag, " +
	"IsLatest, IsDeleteMarker"

// inventoryManifestKey is where the fixture manifest lives in the report bucket.
const inventoryManifestKey = "inv/b/cfg/2026-11-05T00-00Z/manifest.json"

// inventoryCfg is an s3_inventory SourceConfig over bucket "b", prefix "docs/", with the
// report in bucket "reports".
func inventoryCfg() interfaces.SourceConfig {
	cfg := s3Cfg(textAccessKey)

	cfg.Kind = interfaces.ConnectorKindS3Inventory
	cfg.InventoryBucket = textReports
	cfg.InventoryManifestKey = inventoryManifestKey

	return cfg
}

// gzipped returns body gzip-compressed, as S3 Inventory writes its CSV data files.
func gzipped(t *testing.T, body string) []byte {
	t.Helper()

	var buf bytes.Buffer

	w := gzip.NewWriter(&buf)
	_, err := w.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	return buf.Bytes()
}

// inventoryFixture is a report of two data files: five objects under docs/ (one with a
// URL-encoded key), plus rows the listing would not return (another prefix, a delete
// marker, a non-current version).
type inventoryFixture struct {
	// manifest is the manifest.json body.
	manifest []byte
	// files maps each data file key to its gzipped body.
	files map[string][]byte
	// listed are the objects a bucket listing returns for the same objects, in order.
	listed []interfaces.StorageObject
}

// newInventoryFixture builds the report in format.
func newInventoryFixture(t *testing.T, format string) inventoryFixture {
	t.Helper()

	first := strings.Join([]string{
		`"b","docs/a.pdf","10","2026-11-01T10:00:00.000Z","e1","true","false"`,
		`"b","other/x.pdf","11","2026-11-01T10:00:00.000Z","e9","true","false"`,
		`"b","docs/my+report%282%29.pdf","12","2026-11-02T10:00:00.000Z","e2","true","false"`,
		`"b","docs/gone.pdf","0","2026-11-02T10:00:00.000Z","","true","true"`,
	}, textNewline) + textNewline
	second := strings.Join([]string{
		`"b","docs/c.pdf","13","2026-11-03T10:00:00.000Z","e3","true","false"`,
		`"b","docs/c.pdf","9","2026-10-03T10:00:00.000Z","e0","false","false"`,
		`"b","docs/d.pdf","14","2026-11-04T10:00:00.000Z","e4","true","false"`,
		`"b","docs/e.pdf","15","2026-11-05T10:00:00.000Z","e5","true","false"`,
	}, textNewline) + textNewline
	manifest, err := json.Marshal(map[string]any{
		textSourcebucket: textB,
		textFileformat:   format,
		textFileschema:   inventorySchema,
		textFiles: []map[string]any{
			{textKey: textInvData1Csv},
			{textKey: textInvData2Csv},
		},
	})
	require.NoError(t, err)

	obj := func(key string, size int64, day, etag string) interfaces.StorageObject {
		return interfaces.StorageObject{
			Key:          key,
			Size:         size,
			ETag:         `"` + etag + `"`,
			LastModified: "2026-11-" + day + "T10:00:00Z",
			ContentType:  "",
		}
	}

	return inventoryFixture{
		manifest: manifest,
		files: map[string][]byte{
			textInvData1Csv: gzipped(t, first),
			textInvData2Csv: gzipped(t, second),
		},
		listed: []interfaces.StorageObject{
			obj("docs/a.pdf", objInput, "01", "e1"),
			obj("docs/my report(2).pdf", objInputFixture, "02", "e2"),
			obj("docs/c.pdf", objInputInventoryFixture, "03", "e3"),
			obj("docs/d.pdf", objInputNewInventoryFixture, "04", "e4"),
			obj("docs/e.pdf", inventoryObjectEBytes, "05", "e5"),
		},
	}
}

// serve makes sc answer Downloads from the fixture's report bucket, counting each key.
func (f inventoryFixture) serve(sc *mocks.MockStorageClient, downloads map[string]int) {
	sc.EXPECT().Download(gomock.Any(), textReports, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, key string) (io.ReadCloser, error) {
			downloads[key]++
			if key == inventoryManifestKey {
				return io.NopCloser(bytes.NewReader(f.manifest)), nil
			}

			return io.NopCloser(bytes.NewReader(f.files[key])), nil
		}).AnyTimes()
}

// drain pages src from token with limit until exhausted, returning every object.
func drain(
	t *testing.T,
	src interfaces.ConnectorSource,
	token string,
	limit int,
) []interfaces.StorageObject {
	t.Helper()

	var all []interfaces.StorageObject

	for {
		page, next, err := src.ListPage(context.Background(), token, limit)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page), limit)

		all = append(all, page...)
		if next == "" {
			return all
		}

		token = next
	}
}

// TestS3Inventory_PagesMatchTheS3Kind tests that the inventory source emits the objects
// the s3 kind lists for the same bucket, page by page.
//
// Why this test is important:
//   - The inventory kind replaces a billion-key LIST crawl; the sync downstream must not
//     tell the two apart, or switching kinds would re-ingest or drop documents.
//
// What it tests:
//   - draining the fixture report in pages of two yields exactly the objects (key URL-
//     decoded, ETag quoted, RFC 3339 time, size) that the s3 kind's ListPage returns for
//     them, skipping another prefix, a delete marker and a non-current version
//   - each data file is downloaded once across the whole crawl (the open file is reused)
//   - TestConnection reports the first page's count
func TestS3Inventory_PagesMatchTheS3Kind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newInventoryFixture(t, textCsv)
	ctrl := gomock.NewController(t)
	storageClient := mocks.NewMockStorageClient(ctrl)
	downloads := map[string]int{}
	fixture.serve(storageClient, downloads)
	storageClient.EXPECT().ListObjectsPageToken(gomock.Any(), textB, textDocs, "", 1000).
		Return(fixture.listed, "", nil)

	sourceBuilder := connector.NewSourceBuilder(
		stdx.Zero[connector.Config](),
		connector.Deps{
			BuildS3Client: stubBuilder(storageClient, nil),
			Decorations:   decorate.Decorations{},
			STS:           nil,
			GitHub:        nil,
		},
	)
	inventory, err := sourceBuilder.Build(ctx, new(inventoryCfg()))
	require.NoError(t, err)

	listing, err := sourceBuilder.Build(ctx, new(s3Cfg(textAccessKey)))
	require.NoError(t, err)

	listed, _, err := listing.ListPage(ctx, "", listPageInputKind)
	require.NoError(t, err)

	got := drain(t, inventory, "", drainInput)

	require.Equal(t, listed, got)
	require.Equal(t, map[string]int{
		inventoryManifestKey: 1, textInvData1Csv: 1, textInvData2Csv: 1,
	}, downloads)

	n, err := inventory.TestConnection(ctx)
	require.NoError(t, err)
	require.Equal(t, len(fixture.listed), n)
}

// TestS3Inventory_ResumesFromACheckpoint tests that a fresh source resumed from a page's
// token yields only the remainder.
//
// Why this test is important:
//   - A crashed sync restarts with a new source and its last committed token; it must not
//     re-list what it already ingested or skip what it had not.
//
// What it tests:
//   - the token after the first page of two, handed to a new source, yields the other
//     three objects; together they are the whole report
//   - the token after three objects points inside the second data file ("1:1")
func TestS3Inventory_ResumesFromACheckpoint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newInventoryFixture(t, textCsv)
	newSource := func() *s3inventory.Source {
		storageClient := mocks.NewMockStorageClient(gomock.NewController(t))
		fixture.serve(storageClient, map[string]int{})

		src, err := s3inventory.NewSource(
			ctx,
			new(inventoryCfg()),
			nil,
			stubBuilder(storageClient, nil),
		)
		require.NoError(t, err)

		return src
	}

	first, token, err := newSource().ListPage(ctx, "", listPageInputACheckpoint)
	require.NoError(t, err)

	rest := drain(t, newSource(), token, drainInput)
	_, afterThree, err := newSource().ListPage(ctx, "", listPageInputFromACheckpoint)
	require.NoError(t, err)

	require.Equal(t, fixture.listed[:2], first)
	require.Equal(t, fixture.listed[2:], rest)
	require.Equal(t, "1:1", afterThree)
}

// TestS3Inventory_RefusesUnusableReportsAndTokens tests the coded failures of the
// inventory source.
//
// Why this test is important:
//   - A misconfigured report (wrong format, another bucket, no ETag or Size column) or a
//     corrupt checkpoint must fail as a terminal configuration error the operator can
//     fix, and an unreachable report bucket as a transient one the sync retries. Without
//     ETags every crawl would read as changed; without sizes the oversized-object cap
//     would never fire.
//
// What it tests:
//   - no manifest key → CodeInvalidInput at build; a Parquet report, a report of another
//     bucket, a report missing the ETag or the Size column (naming it) and a malformed
//     token → CodeInvalidInput; a transport failure reading the manifest →
//     CodeUnavailable
func TestS3Inventory_RefusesUnusableReportsAndTokens(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	source := func(manifest []byte, downloadErr error) *s3inventory.Source {
		storageClient := mocks.NewMockStorageClient(gomock.NewController(t))
		storageClient.EXPECT().Download(gomock.Any(), textReports, inventoryManifestKey).
			Return(io.NopCloser(bytes.NewReader(manifest)), downloadErr).AnyTimes()

		src, err := s3inventory.NewSource(
			ctx,
			new(inventoryCfg()),
			nil,
			stubBuilder(storageClient, nil),
		)
		require.NoError(t, err)

		return src
	}
	otherBucket := newInventoryFixture(t, textCsv).manifest

	otherBucket = bytes.Replace(
		otherBucket,
		[]byte(`"sourceBucket":"b"`),
		[]byte(`"sourceBucket":"x"`),
		1,
	)

	noKey := inventoryCfg()

	noKey.InventoryManifestKey = ""

	testS3InventoryRefusesUnusableReportsAndTokensBadParquetAndTokens(
		ctx, t, source, otherBucket, &noKey,
	)
}

// testS3InventoryRefusesUnusableReportsAndTokensBadParquetAndTokens continues
// TestS3Inventory_RefusesUnusableReportsAndTokens: bad parquet and tokens.
func testS3InventoryRefusesUnusableReportsAndTokensBadParquetAndTokens(
	ctx context.Context,
	t *testing.T,
	source func(manifest []byte, downloadErr error) *s3inventory.Source,
	otherBucket []byte,
	noKeyRef *interfaces.SourceConfig,
) {
	t.Helper()

	noKey := *noKeyRef

	_, buildErr := s3inventory.NewSource(ctx, &noKey, nil, stubBuilder(nil, nil))
	_, _, parquetErr := source(
		newInventoryFixture(t, "Parquet").manifest,
		nil,
	).ListPage(ctx, "", listPageInputTokens)
	_, _, bucketErr := source(otherBucket, nil).ListPage(ctx, "", listPageInputTokens)
	_, _, tokenErr := source(
		newInventoryFixture(t, textCsv).manifest,
		nil,
	).ListPage(ctx, "x:1", listPageInputTokens)
	_, _, downErr := source(
		nil,
		apperr.Sentinel("dial tcp: connection refused"),
	).ListPage(ctx, "", listPageInputTokens)
	without := func(column string) []byte {
		return bytes.Replace(newInventoryFixture(t, textCsv).manifest,
			[]byte(column+", "), nil, 1)
	}
	_, _, noETagErr := source(without("ETag"), nil).ListPage(ctx, "", listPageInputTokens)
	_, _, noSizeErr := source(without("Size"), nil).ListPage(ctx, "", listPageInputTokens)

	require.True(t, apperr.Is(buildErr, apperr.CodeInvalidInput))
	require.True(t, apperr.Is(parquetErr, apperr.CodeInvalidInput))
	require.ErrorContains(t, parquetErr, textCsv)
	require.True(t, apperr.Is(bucketErr, apperr.CodeInvalidInput))
	require.True(t, apperr.Is(tokenErr, apperr.CodeInvalidInput))
	require.True(t, apperr.Is(downErr, apperr.CodeUnavailable))
	require.True(t, apperr.Is(noETagErr, apperr.CodeInvalidInput))
	require.ErrorContains(t, noETagErr, "ETag")
	require.True(t, apperr.Is(noSizeErr, apperr.CodeInvalidInput))
	require.ErrorContains(t, noSizeErr, "Size")
}

// TestConnectorKind_S3StaysTheDefault tests that adding the inventory kind leaves an S3
// connector on the bucket listing.
//
// Why this test is important:
//   - The inventory kind is opt-in; an existing connector (kind "s3", no report fields)
//     must keep listing its bucket and must never try to read a report.
//
// What it tests:
//   - the kinds' wire values are "s3" and "s3_inventory"
//   - an "s3" config with no inventory fields builds, and its TestConnection lists the
//     bucket (ListObjectsPage) with no Download
func TestConnectorKind_S3StaysTheDefault(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	storageClient := mocks.NewMockStorageClient(gomock.NewController(t))
	storageClient.EXPECT().ListObjectsPage(gomock.Any(), textB, textDocs, gomock.Any()).
		Return([]interfaces.StorageObject{fixtures.Build(func(row *interfaces.StorageObject) {
			row.Key = textDocsA
		})}, nil)
	storageClient.EXPECT().Download(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	sourceBuilder := connector.NewSourceBuilder(
		stdx.Zero[connector.Config](),
		connector.Deps{
			BuildS3Client: stubBuilder(storageClient, nil),
			Decorations:   decorate.Decorations{},
			STS:           nil,
			GitHub:        nil,
		},
	)

	src, err := sourceBuilder.Build(ctx, new(s3Cfg(textAccessKey)))
	require.NoError(t, err)

	n, err := src.TestConnection(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, "s3", string(interfaces.ConnectorKindS3))
	require.Equal(
		t,
		"s3_inventory",
		string(interfaces.ConnectorKindS3Inventory),
	)
}

// failingReader fails every read with err (a body whose request context has ended).
type failingReader struct {
	// err is returned by every Read.
	err error
}

// Read always fails.
func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

// TestS3Inventory_RetriesADeadBodyOnce tests that a data file left open by an earlier
// page is downloaded again when its body fails, and the page is still correct.
//
// Why this test is important:
//   - The open body belongs to the previous call's request; once that context ends its
//     reads fail. Failing the crawl there would stall every sync on its second page.
//
// What it tests:
//   - an uncompressed data file whose first body dies after two rows: the second page
//     (resuming inside it) re-downloads the file, skips the consumed row and returns the
//     right two objects; the file is downloaded twice
func TestS3Inventory_RetriesADeadBodyOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rows := `"b","docs/1","1","2026-11-01T10:00:00.000Z","e1","true","false"` + textNewline +
		`"b","docs/2","2","2026-11-01T10:00:00.000Z","e2","true","false"` + textNewline
	full := rows + `"b","docs/3","3","2026-11-01T10:00:00.000Z","e3","true","false"` +
		textNewline
	manifest, err := json.Marshal(map[string]any{
		textSourcebucket: textB, textFileformat: textCsv, textFileschema: inventorySchema,
		textFiles: []map[string]any{{textKey: "inv/data/plain.csv"}},
	})
	require.NoError(t, err)

	downloads := 0
	storageClient := mocks.NewMockStorageClient(gomock.NewController(t))
	storageClient.EXPECT().Download(gomock.Any(), textReports, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, key string) (io.ReadCloser, error) {
			if key == inventoryManifestKey {
				return io.NopCloser(bytes.NewReader(manifest)), nil
			}

			downloads++
			if downloads == 1 {
				dead := failingReader{err: apperr.Sentinel("connection reset")}

				return io.NopCloser(io.MultiReader(strings.NewReader(rows), dead)), nil
			}

			return io.NopCloser(strings.NewReader(full)), nil
		}).AnyTimes()

	src, err := s3inventory.NewSource(
		ctx,
		new(inventoryCfg()),
		nil,
		stubBuilder(storageClient, nil),
	)
	require.NoError(t, err)

	first, token, err := src.ListPage(ctx, "", 1)
	require.NoError(t, err)

	second, next, err := src.ListPage(ctx, token, listPageInputOnce)
	require.NoError(t, err)

	require.Equal(t, "docs/1", first[0].Key)
	require.Equal(t, []string{"docs/2", "docs/3"}, []string{second[0].Key, second[1].Key})
	require.Empty(t, next)
	require.Equal(t, wantDownloads, downloads)
}

// TestS3Inventory_RefusesMalformedDataFiles tests that a report the source cannot read
// row by row fails as a terminal configuration error.
//
// Why this test is important:
//   - A corrupt data file or a checkpoint past a file's end can only be fixed by an
//     operator (a new report, a reset sync); retrying it forever would hide the fault.
//
// What it tests:
//   - an unbalanced quote, a non-numeric size, a token past the file's end and a schema
//     without a Key column each raise CodeInvalidInput
func TestS3Inventory_RefusesMalformedDataFiles(t *testing.T) {
	t.Parallel()

	good := `"b","docs/1","1","2026-11-01T10:00:00.000Z","e1","true","false"` + textNewline
	cases := []struct {
		name, schema, data, token string
	}{
		{"unbalanced quote", inventorySchema, `"b","docs/1` + textNewline, ""},
		{
			"non-numeric size",
			inventorySchema,
			`"b","docs/1","ten","","","true","false"` + textNewline,
			"",
		},
		{"token past the end", inventorySchema, good, "0:5"},
		{"no key column", "Bucket, Size", `"b","1"` + textNewline, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			testS3InventoryRefusesMalformedDataFilesCase(t, tc)
		})
	}
}

// testS3InventoryRefusesMalformedDataFilesCase is the body of a closure in
// TestS3Inventory_RefusesMalformedDataFiles.
func testS3InventoryRefusesMalformedDataFilesCase(t *testing.T, testCase struct {
	name   string
	schema string
	data   string
	token  string
},
) {
	t.Helper()

	ctx := context.Background()
	manifest, err := json.Marshal(map[string]any{
		textSourcebucket: textB, textFileformat: textCsv, textFileschema: testCase.schema,
		textFiles: []map[string]any{{textKey: "inv/data/plain.csv"}},
	})
	require.NoError(t, err)

	storageClient := mocks.NewMockStorageClient(gomock.NewController(t))
	storageClient.EXPECT().Download(gomock.Any(), textReports, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, key string) (io.ReadCloser, error) {
			if key == inventoryManifestKey {
				return io.NopCloser(bytes.NewReader(manifest)), nil
			}

			return io.NopCloser(strings.NewReader(testCase.data)), nil
		}).
		AnyTimes()

	src, err := s3inventory.NewSource(
		ctx,
		new(inventoryCfg()),
		nil,
		stubBuilder(storageClient, nil),
	)
	require.NoError(t, err)

	_, _, err = src.ListPage(ctx, testCase.token, listPageInput)

	require.True(t, apperr.Is(err, apperr.CodeInvalidInput), textGotV, err)
}

const (
	// listPageInputACheckpoint is the limit passed to ListPage in
	// TestS3Inventory_ResumesFromACheckpoint.
	listPageInputACheckpoint = 2

	// listPageInputFromACheckpoint is the limit passed to ListPage in
	// TestS3Inventory_ResumesFromACheckpoint.
	listPageInputFromACheckpoint = 3

	// listPageInputKind is the limit passed to ListPage in
	// TestS3Inventory_PagesMatchTheS3Kind.
	listPageInputKind = 1000

	// listPageInputOnce is the limit passed to ListPage in
	// TestS3Inventory_RetriesADeadBodyOnce.
	listPageInputOnce = 2

	// listPageInputTokens is the limit passed to ListPage in
	// testS3InventoryRefusesUnusableReportsAndTokensBadParquetAndTokens.
	listPageInputTokens = 2

	// objInput is the size passed to obj in newInventoryFixture.
	objInput = 10

	// objInputFixture is the size passed to obj in newInventoryFixture.
	objInputFixture = 12

	// objInputInventoryFixture is the size passed to obj in newInventoryFixture.
	objInputInventoryFixture = 13

	// objInputNewInventoryFixture is the size passed to obj in newInventoryFixture.
	objInputNewInventoryFixture = 14

	// inventoryObjectEBytes is the size passed to obj in newInventoryFixture.
	inventoryObjectEBytes = 15

	// wantDownloads is the expected downloads.
	wantDownloads = 2
)

const (
	// textFileformat is the literal "fileFormat".
	textFileformat = "fileFormat"

	// textFileschema is the literal "fileSchema".
	textFileschema = "fileSchema"

	// textInvData1Csv is the literal "inv/data/1.csv.gz".
	textInvData1Csv = "inv/data/1.csv.gz"

	// textInvData2Csv is the literal "inv/data/2.csv.gz".
	textInvData2Csv = "inv/data/2.csv.gz"

	// textSourcebucket is the literal "sourceBucket".
	textSourcebucket = "sourceBucket"
)

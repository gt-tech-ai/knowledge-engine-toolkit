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

	connector "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3inventory"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
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
	cfg := s3Cfg("access_key")

	cfg.Kind = interfaces.ConnectorKindS3Inventory
	cfg.InventoryBucket = "reports"
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
	}, "\n") + "\n"
	second := strings.Join([]string{
		`"b","docs/c.pdf","13","2026-11-03T10:00:00.000Z","e3","true","false"`,
		`"b","docs/c.pdf","9","2026-10-03T10:00:00.000Z","e0","false","false"`,
		`"b","docs/d.pdf","14","2026-11-04T10:00:00.000Z","e4","true","false"`,
		`"b","docs/e.pdf","15","2026-11-05T10:00:00.000Z","e5","true","false"`,
	}, "\n") + "\n"
	manifest, err := json.Marshal(map[string]any{
		"sourceBucket": "b",
		"fileFormat":   format,
		"fileSchema":   inventorySchema,
		"files": []map[string]any{
			{"key": "inv/data/1.csv.gz"},
			{"key": "inv/data/2.csv.gz"},
		},
	})
	require.NoError(t, err)

	obj := func(key string, size int64, day, etag string) interfaces.StorageObject {
		return interfaces.StorageObject{
			Key:          key,
			Size:         size,
			ETag:         `"` + etag + `"`,
			LastModified: "2026-11-" + day + "T10:00:00Z",
		}
	}

	return inventoryFixture{
		manifest: manifest,
		files: map[string][]byte{
			"inv/data/1.csv.gz": gzipped(t, first),
			"inv/data/2.csv.gz": gzipped(t, second),
		},
		listed: []interfaces.StorageObject{
			obj("docs/a.pdf", 10, "01", "e1"),
			obj("docs/my report(2).pdf", 12, "02", "e2"),
			obj("docs/c.pdf", 13, "03", "e3"),
			obj("docs/d.pdf", 14, "04", "e4"),
			obj("docs/e.pdf", 15, "05", "e5"),
		},
	}
}

// serve makes sc answer Downloads from the fixture's report bucket, counting each key.
func (f inventoryFixture) serve(sc *mocks.MockStorageClient, downloads map[string]int) {
	sc.EXPECT().Download(gomock.Any(), "reports", gomock.Any()).
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
	fixture := newInventoryFixture(t, "CSV")
	ctrl := gomock.NewController(t)
	sc := mocks.NewMockStorageClient(ctrl)
	downloads := map[string]int{}
	fixture.serve(sc, downloads)
	sc.EXPECT().ListObjectsPageToken(gomock.Any(), "b", "docs/", "", 1000).
		Return(fixture.listed, "", nil)

	b := connector.NewSourceBuilder(connector.Config{}, connector.Deps{
		BuildS3Client: stubBuilder(sc, nil),
	})
	inventory, err := b.Build(ctx, inventoryCfg())
	require.NoError(t, err)

	listing, err := b.Build(ctx, s3Cfg("access_key"))
	require.NoError(t, err)

	listed, _, err := listing.ListPage(ctx, "", 1000)
	require.NoError(t, err)

	got := drain(t, inventory, "", 2)

	require.Equal(t, listed, got)
	require.Equal(t, map[string]int{
		inventoryManifestKey: 1, "inv/data/1.csv.gz": 1, "inv/data/2.csv.gz": 1,
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
	fixture := newInventoryFixture(t, "CSV")
	newSource := func() *s3inventory.Source {
		sc := mocks.NewMockStorageClient(gomock.NewController(t))
		fixture.serve(sc, map[string]int{})

		src, err := s3inventory.NewSource(ctx, inventoryCfg(), nil, stubBuilder(sc, nil))
		require.NoError(t, err)

		return src
	}

	first, token, err := newSource().ListPage(ctx, "", 2)
	require.NoError(t, err)

	rest := drain(t, newSource(), token, 2)
	_, afterThree, err := newSource().ListPage(ctx, "", 3)
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
		sc := mocks.NewMockStorageClient(gomock.NewController(t))
		sc.EXPECT().Download(gomock.Any(), "reports", inventoryManifestKey).
			Return(io.NopCloser(bytes.NewReader(manifest)), downloadErr).AnyTimes()

		src, err := s3inventory.NewSource(ctx, inventoryCfg(), nil, stubBuilder(sc, nil))
		require.NoError(t, err)

		return src
	}
	otherBucket := newInventoryFixture(t, "CSV").manifest

	otherBucket = bytes.Replace(
		otherBucket,
		[]byte(`"sourceBucket":"b"`),
		[]byte(`"sourceBucket":"x"`),
		1,
	)

	noKey := inventoryCfg()

	noKey.InventoryManifestKey = ""

	_, buildErr := s3inventory.NewSource(ctx, noKey, nil, stubBuilder(nil, nil))
	_, _, parquetErr := source(
		newInventoryFixture(t, "Parquet").manifest,
		nil,
	).ListPage(ctx, "", 2)
	_, _, bucketErr := source(otherBucket, nil).ListPage(ctx, "", 2)
	_, _, tokenErr := source(
		newInventoryFixture(t, "CSV").manifest,
		nil,
	).ListPage(ctx, "x:1", 2)
	_, _, downErr := source(
		nil,
		apperr.Sentinel("dial tcp: connection refused"),
	).ListPage(ctx, "", 2)
	without := func(column string) []byte {
		return bytes.Replace(newInventoryFixture(t, "CSV").manifest,
			[]byte(column+", "), nil, 1)
	}
	_, _, noETagErr := source(without("ETag"), nil).ListPage(ctx, "", 2)
	_, _, noSizeErr := source(without("Size"), nil).ListPage(ctx, "", 2)

	require.True(t, apperr.Is(buildErr, apperr.CodeInvalidInput))
	require.True(t, apperr.Is(parquetErr, apperr.CodeInvalidInput))
	require.ErrorContains(t, parquetErr, "CSV")
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
	sc := mocks.NewMockStorageClient(gomock.NewController(t))
	sc.EXPECT().ListObjectsPage(gomock.Any(), "b", "docs/", gomock.Any()).
		Return([]interfaces.StorageObject{{Key: "docs/a"}}, nil)
	sc.EXPECT().Download(gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	b := connector.NewSourceBuilder(connector.Config{}, connector.Deps{
		BuildS3Client: stubBuilder(sc, nil),
	})

	src, err := b.Build(ctx, s3Cfg("access_key"))
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
	rows := `"b","docs/1","1","2026-11-01T10:00:00.000Z","e1","true","false"` + "\n" +
		`"b","docs/2","2","2026-11-01T10:00:00.000Z","e2","true","false"` + "\n"
	full := rows + `"b","docs/3","3","2026-11-01T10:00:00.000Z","e3","true","false"` + "\n"
	manifest, err := json.Marshal(map[string]any{
		"sourceBucket": "b", "fileFormat": "CSV", "fileSchema": inventorySchema,
		"files": []map[string]any{{"key": "inv/data/plain.csv"}},
	})
	require.NoError(t, err)

	downloads := 0
	sc := mocks.NewMockStorageClient(gomock.NewController(t))
	sc.EXPECT().Download(gomock.Any(), "reports", gomock.Any()).
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

	src, err := s3inventory.NewSource(ctx, inventoryCfg(), nil, stubBuilder(sc, nil))
	require.NoError(t, err)

	first, token, err := src.ListPage(ctx, "", 1)
	require.NoError(t, err)

	second, next, err := src.ListPage(ctx, token, 2)
	require.NoError(t, err)

	require.Equal(t, "docs/1", first[0].Key)
	require.Equal(t, []string{"docs/2", "docs/3"}, []string{second[0].Key, second[1].Key})
	require.Empty(t, next)
	require.Equal(t, 2, downloads)
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

	good := `"b","docs/1","1","2026-11-01T10:00:00.000Z","e1","true","false"` + "\n"
	cases := []struct {
		name, schema, data, token string
	}{
		{"unbalanced quote", inventorySchema, `"b","docs/1` + "\n", ""},
		{
			"non-numeric size",
			inventorySchema,
			`"b","docs/1","ten","","","true","false"` + "\n",
			"",
		},
		{"token past the end", inventorySchema, good, "0:5"},
		{"no key column", "Bucket, Size", `"b","1"` + "\n", ""},
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
func testS3InventoryRefusesMalformedDataFilesCase(t *testing.T, tc struct {
	name   string
	schema string
	data   string
	token  string
},
) {
	t.Helper()

	ctx := context.Background()
	manifest, err := json.Marshal(map[string]any{
		"sourceBucket": "b", "fileFormat": "CSV", "fileSchema": tc.schema,
		"files": []map[string]any{{"key": "inv/data/plain.csv"}},
	})
	require.NoError(t, err)

	sc := mocks.NewMockStorageClient(gomock.NewController(t))
	sc.EXPECT().Download(gomock.Any(), "reports", gomock.Any()).
		DoAndReturn(func(_ context.Context, _, key string) (io.ReadCloser, error) {
			if key == inventoryManifestKey {
				return io.NopCloser(bytes.NewReader(manifest)), nil
			}

			return io.NopCloser(strings.NewReader(tc.data)), nil
		}).
		AnyTimes()

	src, err := s3inventory.NewSource(
		ctx,
		inventoryCfg(),
		nil,
		stubBuilder(sc, nil),
	)
	require.NoError(t, err)

	_, _, err = src.ListPage(ctx, tc.token, 10)

	require.True(t, apperr.Is(err, apperr.CodeInvalidInput), "got %v", err)
}

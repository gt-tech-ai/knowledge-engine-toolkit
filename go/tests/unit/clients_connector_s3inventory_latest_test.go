package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/connector/s3inventory"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// inventoryPrefix is the fixture report's configuration folder
// (destination-prefix/source-bucket/config-ID/).
const inventoryPrefix = "inv/b/cfg/"

// prefixCfg is inventoryCfg naming the report's folder instead of one manifest.
func prefixCfg() interfaces.SourceConfig {
	cfg := inventoryCfg()

	cfg.InventoryManifestKey = ""
	cfg.InventoryPrefix = inventoryPrefix

	return cfg
}

// reportKeys lists the report folder as S3 Inventory leaves it, over two listing pages:
// a complete report of 11-04, the complete report of 11-05 (the fixture's manifest), a
// report of 11-06 whose checksum is not written yet, data files and a Hive symlink.
func reportKeys(sc *mocks.MockStorageClient) {
	obj := func(key string) interfaces.StorageObject {
		return interfaces.StorageObject{Key: inventoryPrefix + key}
	}
	sc.EXPECT().
		ListObjectsPageToken(gomock.Any(), "reports", inventoryPrefix, "", gomock.Any()).
		Return([]interfaces.StorageObject{
			obj("2026-11-04T00-00Z/manifest.checksum"),
			obj("2026-11-04T00-00Z/manifest.json"),
			obj("2026-11-05T00-00Z/manifest.json"),
			obj("data/1.csv.gz"),
		}, "page2", nil)
	sc.EXPECT().
		ListObjectsPageToken(gomock.Any(), "reports", inventoryPrefix, "page2", gomock.Any()).
		Return([]interfaces.StorageObject{
			obj("2026-11-05T00-00Z/manifest.checksum"),
			obj("2026-11-06T00-00Z/manifest.json"),
			obj("hive/dt=2026-11-06-00-00/symlink.txt"),
		}, "", nil)
}

// TestS3Inventory_ReadsTheNewestCompleteReportUnderAPrefix tests the inventory source
// configured with the report's folder rather than one manifest key.
//
// Why this test is important:
//   - S3 Inventory writes a new dated report each day, so a connector that stored one
//     manifest key would read a stale report from the second day on. A report whose
//     manifest.checksum is not written yet is still being delivered and must not be read.
//   - A sync that crashes and resumes after the next report lands must finish the report
//     it started: the token's file and row indexes mean nothing in another report.
//
// What it tests:
//   - with no manifest key, the source reads the newest report that has a checksum
//     (11-05, not the incomplete 11-06), and drains to the same objects as the fixture
//   - its tokens name that report ("2026-11-05T00-00Z/1:1" after three objects), and a
//     fresh source resumed from one reads that report without listing the folder
//   - a folder with no complete report is CodeNotFound; a config with neither a manifest
//     key nor a folder is CodeInvalidInput at build
func TestS3Inventory_ReadsTheNewestCompleteReportUnderAPrefix(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newInventoryFixture(t, "CSV")
	source := func(list func(*mocks.MockStorageClient)) *s3inventory.Source {
		sc := mocks.NewMockStorageClient(gomock.NewController(t))
		fixture.serve(sc, map[string]int{})
		list(sc)

		src, err := s3inventory.NewSource(ctx, prefixCfg(), nil, stubBuilder(sc, nil))
		require.NoError(t, err)

		return src
	}

	_, afterThree, err := source(reportKeys).ListPage(ctx, "", 3)
	require.NoError(t, err)

	all := drain(t, source(reportKeys), "", 2)
	rest := drain(t, source(func(*mocks.MockStorageClient) {}), afterThree, 3)
	_, _, emptyErr := source(func(sc *mocks.MockStorageClient) {
		sc.EXPECT().
			ListObjectsPageToken(gomock.Any(), "reports", inventoryPrefix, "", gomock.Any()).
			Return([]interfaces.StorageObject{
				{Key: inventoryPrefix + "2026-11-06T00-00Z/manifest.json"},
			}, "", nil)
	}).ListPage(ctx, "", 2)
	neither := prefixCfg()

	neither.InventoryPrefix = ""

	_, buildErr := s3inventory.NewSource(ctx, neither, nil, stubBuilder(nil, nil))

	require.Equal(t, "2026-11-05T00-00Z/1:1", afterThree)
	require.Equal(t, fixture.listed, all)
	require.Equal(t, fixture.listed[3:], rest)
	require.True(t, apperr.Is(emptyErr, apperr.CodeNotFound), emptyErr)
	require.True(t, apperr.Is(buildErr, apperr.CodeInvalidInput))
}

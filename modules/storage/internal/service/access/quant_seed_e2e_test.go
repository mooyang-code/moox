package access

import (
	"context"
	"path/filepath"
	"testing"

	seedmetadata "github.com/mooyang-code/moox/modules/storage/internal/bootstrap/metadata"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestQuantInitialSeedEndToEnd(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	metadataPath := filepath.Join(root, "metadata.db")
	schemaPath := filepath.Join("..", "..", "..", "schema", "metadata.sql")
	seedPath := filepath.Join("..", "..", "..", "..", "..", "examples", "metadata-quant-initial.seed.yaml")

	result, err := seedmetadata.ImportSeed(ctx, seedmetadata.SeedOptions{
		Storage:    storageconfig.StorageConfig{Metadata: storageconfig.StorageMetadata{Path: metadataPath}},
		SchemaPath: schemaPath,
		SeedPath:   seedPath,
	})
	require.NoError(t, err)
	require.Equal(t, 4, result.Spaces)

	svc := NewServiceWithOptions(Options{Root: root, MetadataPath: metadataPath, InitSchemaPath: schemaPath})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	spaces, err := svc.ListSpaces(ctx, &pb.ListSpacesReq{Page: &pb.Page{Page: 1, Size: 100}})
	mustRetOK(t, spaces, err)
	require.Len(t, spaces.GetSpaces(), 4)

	groups, err := svc.ListFieldGroups(ctx, &pb.ListFieldGroupsReq{SpaceId: "stock_cn", Page: &pb.Page{Page: 1, Size: 100}})
	mustRetOK(t, groups, err)
	require.Len(t, groups.GetFieldGroups(), 5)

	fields, err := svc.ListFields(ctx, &pb.ListFieldsReq{SpaceId: "stock_cn", GroupId: "finance", Page: &pb.Page{Page: 1, Size: 100}})
	mustRetOK(t, fields, err)
	require.Len(t, fields.GetFields(), 12)

	datasets, err := svc.ListDatasets(ctx, &pb.ListDatasetsReq{SpaceId: "stock_cn", Page: &pb.Page{Page: 1, Size: 100}})
	mustRetOK(t, datasets, err)
	require.Len(t, datasets.GetDatasets(), 4)

	summary, err := svc.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: "stock_cn", DatasetId: "financial_summary"})
	mustRetOK(t, summary, err)
	require.Equal(t, pb.DataKind_DATA_KIND_RECORD, summary.GetDataset().GetDataKind())
	columns, err := svc.ListDatasetColumns(ctx, &pb.ListDatasetColumnsReq{SpaceId: "stock_cn", DatasetId: "financial_summary", Page: &pb.Page{Page: 1, Size: 100}})
	mustRetOK(t, columns, err)
	require.Len(t, columns.GetColumns(), 8)

	for _, key := range [][2]string{
		{"stock_cn", "stock_kline_1d_view"}, {"stock_cn", "index_kline_1d_view"},
		{"stock_cn", "financial_metric_view"}, {"stock_cn", "financial_summary_view"},
		{"stock_hk", "stock_kline_1d_view"}, {"stock_us", "stock_kline_1d_view"},
		{"crypto", "binance_spot_1h_view"}, {"crypto", "binance_perpetual_1h_view"},
		{"crypto", "okx_spot_1h_view"}, {"crypto", "okx_perpetual_1h_view"},
	} {
		got, getErr := svc.GetView(ctx, &pb.GetViewReq{SpaceId: key[0], ViewId: key[1]})
		mustRetOK(t, got, getErr)
		updated, updateErr := svc.UpdateView(ctx, &pb.UpdateViewReq{View: got.GetView()})
		mustRetOK(t, updated, updateErr)
	}
}

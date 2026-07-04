package access

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestMetadataMutationRefreshesCacheReader(t *testing.T) {
	ctx := context.Background()
	service := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Fatalf("close service: %v", err)
		}
	})

	spaceRsp, err := service.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId: "crypto",
		Name:    "Crypto",
		Status:  "active",
	}})
	mustRetOK(t, spaceRsp, err)
	dataSourceRsp, err := service.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{
		SpaceId:      "crypto",
		DataSourceId: "binance",
		Name:         "Binance",
		Kind:         "exchange",
		Market:       "crypto",
		Status:       "active",
	}})
	mustRetOK(t, dataSourceRsp, err)
	datasetRsp, err := service.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
		SpaceId:      "crypto",
		DatasetId:    "binance_kline",
		DataSourceId: "binance",
		Name:         "币安K线",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Freqs:        []string{"1m"},
		Status:       "active",
	}})
	mustRetOK(t, datasetRsp, err)

	dataset, err := service.MetadataReader().GetDataset(ctx, "crypto", "binance_kline")
	if err != nil {
		t.Fatalf("get dataset through cache reader after create: %v", err)
	}
	if dataset.GetDatasetId() != "binance_kline" {
		t.Fatalf("dataset_id = %q, want binance_kline", dataset.GetDatasetId())
	}
}

func mustRetOK[T interface{ GetRetInfo() *pb.RetInfo }](t *testing.T, rsp T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	ret := rsp.GetRetInfo()
	if ret == nil || ret.GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %#v, want success", ret)
	}
}

func storageSchemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "schema", "metadata.sql"))
}

package e2e

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// fillDataset 让只关心行的测试请求带上顶层数据集：空着时取第一行的 space_id、dataset_id。
func fillDataset(req *pb.PrimaryUpsertFieldsReq) *pb.PrimaryUpsertFieldsReq {
	if req.GetSpaceId() == "" && req.GetDatasetId() == "" && len(req.GetRows()) > 0 && req.GetRows()[0].GetKey() != nil {
		req.SpaceId, req.DatasetId = req.GetRows()[0].GetKey().GetSpaceId(), req.GetRows()[0].GetKey().GetDatasetId()
	}
	return req
}

// testDatasetSource 是测试用的内存元数据来源，供构造真实的 DatasetCache。
type testDatasetSource struct {
	datasets []*pb.Dataset
	columns  []*pb.DatasetColumn
}

func (s *testDatasetSource) ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	return s.datasets, &pb.PageResult{Total: uint32(len(s.datasets))}, nil
}

func (s *testDatasetSource) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return s.columns, &pb.PageResult{Total: uint32(len(s.columns))}, nil
}

func newTestDatasetCache(t testing.TB, datasets []*pb.Dataset, columns []*pb.DatasetColumn) *metacache.DatasetCache {
	t.Helper()
	cache, err := metacache.NewDatasetCache(context.Background(), &testDatasetSource{datasets: datasets, columns: columns})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

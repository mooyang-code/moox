package primarystore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// fillDataset 让只关心行的测试请求带上顶层数据集：空着时取第一行的 space_id、dataset_id。
func fillDataset(req *pb.PrimaryUpsertFieldsReq) *pb.PrimaryUpsertFieldsReq {
	if req.GetSpaceId() == "" && req.GetDatasetId() == "" && len(req.GetRows()) > 0 && req.GetRows()[0].GetKey() != nil {
		req.SpaceId, req.DatasetId = req.GetRows()[0].GetKey().GetSpaceId(), req.GetRows()[0].GetKey().GetDatasetId()
	}
	return req
}

// activeRequestSnapshot 是任何数据集都存在且启用、但没有列定义的请求快照，只关心读写行的测试用它。
func activeRequestSnapshot() metadata.RequestSnapshot { return activeSnapshot{} }

type activeSnapshot struct{}

func (activeSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	return &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, Status: "active"}, true
}
func (activeSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

// openTestStore 打开一个临时 Pebble 库，测试结束时关闭。
func openTestStore(t *testing.T) *pebble.Store {
	t.Helper()
	store, err := pebble.Open(pebble.Options{NodeID: "storage-primary", Path: filepath.Join(t.TempDir(), "pebble")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// hookStore 在真实 Pebble 库上按需替换个别方法，用于注入失败或记录调用。
type hookStore struct {
	*pebble.Store
	upsert       func(ctx context.Context, spaceID, datasetID, commitID string, rows []*pb.RowFieldUpsert) ([]*pebble.OutboxEntry, error)
	readRows     func(ctx context.Context, keys []*pb.RowKey, columns, attributeKeys []string) ([]*pb.RowFieldValues, error)
	readSeries   func(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
	countSeries  func(ctx context.Context, req *pb.CountTimeSeriesRowsReq) ([]*pb.SubjectRowCount, error)
	readChanges  func(ctx context.Context, req *pb.ReadDatasetChangesReq) (*pb.ReadDatasetChangesRsp, error)
	commit       func(ctx context.Context, exp pebble.DatasetPeriodExpectation, items []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error)
	appendMarker func(ctx context.Context, raw []byte) (string, error)
}

func (s *hookStore) UpsertFields(ctx context.Context, spaceID, datasetID, commitID string, rows []*pb.RowFieldUpsert) ([]*pebble.OutboxEntry, error) {
	if s.upsert != nil {
		return s.upsert(ctx, spaceID, datasetID, commitID, rows)
	}
	return s.Store.UpsertFields(ctx, spaceID, datasetID, commitID, rows)
}

func (s *hookStore) ReadRows(ctx context.Context, keys []*pb.RowKey, columns, attributeKeys []string) ([]*pb.RowFieldValues, error) {
	if s.readRows != nil {
		return s.readRows(ctx, keys, columns, attributeKeys)
	}
	return s.Store.ReadRows(ctx, keys, columns, attributeKeys)
}

func (s *hookStore) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if s.readSeries != nil {
		return s.readSeries(ctx, req)
	}
	return s.Store.ReadTimeSeriesRows(ctx, req)
}

func (s *hookStore) CountTimeSeriesRows(ctx context.Context, req *pb.CountTimeSeriesRowsReq) ([]*pb.SubjectRowCount, error) {
	if s.countSeries != nil {
		return s.countSeries(ctx, req)
	}
	return s.Store.CountTimeSeriesRows(ctx, req)
}

func (s *hookStore) ReadDatasetChanges(ctx context.Context, req *pb.ReadDatasetChangesReq) (*pb.ReadDatasetChangesRsp, error) {
	if s.readChanges != nil {
		return s.readChanges(ctx, req)
	}
	return s.Store.ReadDatasetChanges(ctx, req)
}

func (s *hookStore) CommitTimeSeriesBatch(ctx context.Context, exp pebble.DatasetPeriodExpectation, items []pebble.TimeSeriesBatchItem) (pebble.DatasetPeriodResult, error) {
	if s.commit != nil {
		return s.commit(ctx, exp, items)
	}
	return s.Store.CommitTimeSeriesBatch(ctx, exp, items)
}

func (s *hookStore) AppendDatasetMarker(ctx context.Context, raw []byte) (string, error) {
	if s.appendMarker != nil {
		return s.appendMarker(ctx, raw)
	}
	return s.Store.AppendDatasetMarker(ctx, raw)
}

func newHookStore(t *testing.T) *hookStore {
	return &hookStore{Store: openTestStore(t)}
}

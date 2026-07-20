//go:build legacy_storage

package datashard

import (
	"context"
	contracts "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/contracts"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMergeRowsRequiresRequest(t *testing.T) {
	svc := NewService(Options{Pebble: &primaryTestStore{}})
	defer svc.Close()
	rsp, err := svc.MergeRows(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestMergeRowsWritesRows(t *testing.T) {
	store := &primaryTestStore{}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()
	rows := []*pb.ShardRow{{Key: &pb.ShardKey{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}}
	rsp, err := svc.MergeRows(context.Background(), &pb.MergeRowsReq{
		Target: &pb.ShardTarget{NodeId: "node-1"},
		Rows:   rows,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, rows, store.written)
}

func TestReadRowsReturnsStoredRows(t *testing.T) {
	rows := []*pb.ShardRow{{Key: &pb.ShardKey{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}}
	store := &primaryTestStore{readRows: rows}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()
	rsp, err := svc.ReadRows(context.Background(), &pb.ReadRowsReq{
		Target: &pb.ShardTarget{NodeId: "node-1"},
		Keys:   []*pb.ShardKey{{SpaceId: "crypto", DatasetId: "kline"}},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, rows, rsp.GetRows())
}

func TestScanRowsReturnsStoredRows(t *testing.T) {
	rows := []*pb.ShardRow{{Key: &pb.ShardKey{SpaceId: "crypto", DatasetId: "kline"}}}
	store := &primaryTestStore{scanRows: rows}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()
	rsp, err := svc.ScanRows(context.Background(), &pb.ScanRowsReq{
		Target:   &pb.ShardTarget{NodeId: "node-1"},
		DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, rows, rsp.GetRows())
}

func TestNormalizeReadRowsReqFillsKeyFields(t *testing.T) {
	req := &pb.ReadRowsReq{
		Target: &pb.ShardTarget{SpaceId: "crypto", DatasetId: "kline"},
		Keys:   []*pb.ShardKey{{Key: "BTC|1m|_"}},
	}
	normalized := normalizeReadRowsReq(req)
	assert.Equal(t, "crypto", normalized.GetKeys()[0].GetSpaceId())
	assert.Equal(t, "kline", normalized.GetKeys()[0].GetDatasetId())
}

func TestNormalizeReadRowsReqNilRequest(t *testing.T) {
	normalized := normalizeReadRowsReq(nil)
	require.NotNil(t, normalized)
}

type primaryTestStore struct {
	written       []*pb.ShardRow
	outboxEntry   *contracts.OutboxEntry
	readRows      []*pb.ShardRow
	scanRows      []*pb.ShardRow
	outboxEntries []*contracts.OutboxEntry
}

func (s *primaryTestStore) Close() error { return nil }

func (s *primaryTestStore) WriteRows(_ context.Context, rows []*pb.ShardRow) error {
	s.written = append([]*pb.ShardRow(nil), rows...)
	return nil
}

func (s *primaryTestStore) WriteRowsWithCommittedMessage(ctx context.Context, rows []*pb.ShardRow) error {
	return s.WriteRows(ctx, rows)
}

func (s *primaryTestStore) WriteRowsWithOutbox(_ context.Context, rows []*pb.ShardRow, entry *contracts.OutboxEntry) error {
	s.written = append([]*pb.ShardRow(nil), rows...)
	s.outboxEntry = entry
	return nil
}

func (s *primaryTestStore) ListOutbox(_ context.Context, _ uint64, maxItems int, _ int) ([]*contracts.OutboxEntry, error) {
	if len(s.outboxEntries) > maxItems {
		return s.outboxEntries[:maxItems], nil
	}
	return s.outboxEntries, nil
}

func (s *primaryTestStore) DeleteOutbox(_ context.Context, _ []uint64) error { return nil }

func (s *primaryTestStore) ReadRows(_ context.Context, _ []*pb.ShardKey, _ *pb.VersionRange, _ pb.SortOrder, _ []string, _ *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
	return s.readRows, &pb.PageResult{}, nil
}

func (s *primaryTestStore) ScanRows(context.Context, *pb.ShardTarget, pb.DataKind, *pb.VersionRange, pb.SortOrder, []string, *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
	return s.scanRows, &pb.PageResult{}, nil
}

func (s *primaryTestStore) DeleteRows(context.Context, []*pb.ShardKey) error { return nil }

func TestDeleteRowsRequiresKeys(t *testing.T) {
	svc := NewService(Options{Pebble: &deleteTestStore{}})
	defer svc.Close()
	rsp, err := svc.DeleteRows(context.Background(), &pb.DeleteRowsReq{Target: &pb.ShardTarget{NodeId: "node-1"}})
	if err != nil {
		t.Fatalf("DeleteRows() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret_info = %+v, want invalid_param", rsp.GetRetInfo())
	}
}

func TestDeleteRowsDeletesExactKeys(t *testing.T) {
	store := &deleteTestStore{}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()
	keys := []*pb.ShardKey{{SpaceId: "moox_system", DatasetId: "host_resource_v1", Key: "agent|1m|_", Version: "2026-07-11T00:00:00.000000000Z"}}
	rsp, err := svc.DeleteRows(context.Background(), &pb.DeleteRowsReq{Target: &pb.ShardTarget{NodeId: "node-1"}, Keys: keys})
	if err != nil {
		t.Fatalf("DeleteRows() error = %v", err)
	}
	if rsp.GetDeleted() != 1 || len(store.deleted) != 1 || store.deleted[0].GetVersion() != keys[0].GetVersion() {
		t.Fatalf("deleted=%d keys=%v, want exact key", rsp.GetDeleted(), store.deleted)
	}
}

type deleteTestStore struct {
	deleted []*pb.ShardKey
}

func (s *deleteTestStore) Close() error                                    { return nil }
func (s *deleteTestStore) WriteRows(context.Context, []*pb.ShardRow) error { return nil }
func (s *deleteTestStore) WriteRowsWithOutbox(context.Context, []*pb.ShardRow, *contracts.OutboxEntry) error {
	return nil
}
func (s *deleteTestStore) ListOutbox(context.Context, uint64, int, int) ([]*contracts.OutboxEntry, error) {
	return nil, nil
}
func (s *deleteTestStore) DeleteOutbox(context.Context, []uint64) error { return nil }
func (s *deleteTestStore) ReadRows(context.Context, []*pb.ShardKey, *pb.VersionRange, pb.SortOrder, []string, *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
	return nil, nil, nil
}
func (s *deleteTestStore) ScanRows(context.Context, *pb.ShardTarget, pb.DataKind, *pb.VersionRange, pb.SortOrder, []string, *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
	return nil, nil, nil
}
func (s *deleteTestStore) DeleteRows(_ context.Context, keys []*pb.ShardKey) error {
	s.deleted = append(s.deleted, keys...)
	return nil
}

func TestDeleteRowsDeletesKeys(t *testing.T) {
	store := &primaryTestStore{}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()

	keys := []*pb.ShardKey{{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}
	rsp, err := svc.DeleteRows(context.Background(), &pb.DeleteRowsReq{
		Target: &pb.ShardTarget{NodeId: "node-1"},
		Keys:   keys,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, uint32(1), rsp.GetDeleted())
}

func TestDeleteRowsRejectsMissingKeys(t *testing.T) {
	svc := NewService(Options{Pebble: &primaryTestStore{}})
	defer svc.Close()
	rsp, err := svc.DeleteRows(context.Background(), &pb.DeleteRowsReq{Target: &pb.ShardTarget{}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestNormalizeScanRowsReqClonesRequest(t *testing.T) {
	req := &pb.ScanRowsReq{Target: &pb.ShardTarget{SpaceId: "crypto"}}
	normalized := normalizeScanRowsReq(req)
	assert.Equal(t, "crypto", normalized.GetTarget().GetSpaceId())
	assert.NotSame(t, req, normalized)
}

func TestNormalizeScanRowsReqNilRequest(t *testing.T) {
	normalized := normalizeScanRowsReq(nil)
	require.NotNil(t, normalized)
}

func TestServiceCloseHandlesNilService(t *testing.T) {
	var svc *Service
	assert.NoError(t, svc.Close())
}

func TestRetInfoErrorReturnsNilForSuccess(t *testing.T) {
	assert.NoError(t, retInfoError(&pb.RetInfo{Code: pb.ErrorCode_SUCCESS}))
}

func TestRetInfoErrorFormatsFailure(t *testing.T) {
	err := retInfoError(&pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "bad"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INVALID_PARAM")
}

package primary

import (
	"context"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
)

func TestWritePrimaryRowsRequiresRequest(t *testing.T) {
	svc := NewService(Options{Pebble: &primaryTestStore{}})
	defer svc.Close()
	rsp, err := svc.WritePrimaryRows(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestWritePrimaryRowsWritesRows(t *testing.T) {
	store := &primaryTestStore{}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()
	rows := []*pb.PrimaryStoreRow{{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}}
	rsp, err := svc.WritePrimaryRows(context.Background(), &pb.WritePrimaryRowsReq{
		Target: &pb.PrimaryStoreTarget{NodeId: "node-1"},
		Rows:   rows,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, rows, store.written)
}

func TestWritePrimaryRowsValidatesOutboxMessage(t *testing.T) {
	store := &primaryTestStore{}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()
	rsp, err := svc.WritePrimaryRows(context.Background(), &pb.WritePrimaryRowsReq{
		Target:        &pb.PrimaryStoreTarget{NodeId: "node-1"},
		Rows:          []*pb.PrimaryStoreRow{{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline"}}},
		OutboxMessage: []byte("invalid"),
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestWritePrimaryRowsWithOutboxMessage(t *testing.T) {
	store := &primaryTestStore{supportsOutbox: true}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()
	msg := testOutboxMessage(t, "node-1")
	rsp, err := svc.WritePrimaryRows(context.Background(), &pb.WritePrimaryRowsReq{
		Target:        &pb.PrimaryStoreTarget{NodeId: "node-1"},
		Rows:          []*pb.PrimaryStoreRow{{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_"}}},
		OutboxMessage: msg,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	assert.Nil(t, store.outboxEntry)
}

func TestReadPrimaryRowsReturnsStoredRows(t *testing.T) {
	rows := []*pb.PrimaryStoreRow{{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}}
	store := &primaryTestStore{readRows: rows}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()
	rsp, err := svc.ReadPrimaryRows(context.Background(), &pb.ReadPrimaryRowsReq{
		Target: &pb.PrimaryStoreTarget{NodeId: "node-1"},
		Keys:   []*pb.PrimaryStoreKey{{SpaceId: "crypto", DatasetId: "kline"}},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, rows, rsp.GetRows())
}

func TestScanPrimaryRowsReturnsStoredRows(t *testing.T) {
	rows := []*pb.PrimaryStoreRow{{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline"}}}
	store := &primaryTestStore{scanRows: rows}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()
	rsp, err := svc.ScanPrimaryRows(context.Background(), &pb.ScanPrimaryRowsReq{
		Target:   &pb.PrimaryStoreTarget{NodeId: "node-1"},
		DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, rows, rsp.GetRows())
}

func TestNormalizeReadPrimaryRowsReqFillsKeyFields(t *testing.T) {
	req := &pb.ReadPrimaryRowsReq{
		Target: &pb.PrimaryStoreTarget{SpaceId: "crypto", DatasetId: "kline"},
		Keys:   []*pb.PrimaryStoreKey{{Key: "BTC|1m|_"}},
	}
	normalized := normalizeReadPrimaryRowsReq(req)
	assert.Equal(t, "crypto", normalized.GetKeys()[0].GetSpaceId())
	assert.Equal(t, "kline", normalized.GetKeys()[0].GetDatasetId())
}

func TestNormalizeReadPrimaryRowsReqNilRequest(t *testing.T) {
	normalized := normalizeReadPrimaryRowsReq(nil)
	require.NotNil(t, normalized)
}

func TestValidateOutboxMessageRejectsMismatchedNode(t *testing.T) {
	msg := testOutboxMessage(t, "other-node")
	err := validateOutboxMessage(&pb.WritePrimaryRowsReq{
		Target:        &pb.PrimaryStoreTarget{NodeId: "node-1"},
		OutboxMessage: msg,
	})
	require.Error(t, err)
}

type primaryTestStore struct {
	written        []*pb.PrimaryStoreRow
	outboxEntry    *device.OutboxEntry
	readRows       []*pb.PrimaryStoreRow
	scanRows       []*pb.PrimaryStoreRow
	supportsOutbox bool
	outboxEntries  []*device.OutboxEntry
}

func (s *primaryTestStore) Close() error { return nil }

func (s *primaryTestStore) WriteRows(_ context.Context, rows []*pb.PrimaryStoreRow) error {
	s.written = append([]*pb.PrimaryStoreRow(nil), rows...)
	return nil
}

func (s *primaryTestStore) WriteRowsWithOutbox(_ context.Context, rows []*pb.PrimaryStoreRow, entry *device.OutboxEntry) error {
	s.written = append([]*pb.PrimaryStoreRow(nil), rows...)
	s.outboxEntry = entry
	return nil
}

func (s *primaryTestStore) ListOutbox(_ context.Context, _ uint64, maxItems int, _ int) ([]*device.OutboxEntry, error) {
	if len(s.outboxEntries) > maxItems {
		return s.outboxEntries[:maxItems], nil
	}
	return s.outboxEntries, nil
}

func (s *primaryTestStore) DeleteOutbox(_ context.Context, _ []uint64) error { return nil }

func (s *primaryTestStore) ReadRows(_ context.Context, _ []*pb.PrimaryStoreKey, _ *pb.VersionRange, _ pb.SortOrder, _ []string, _ *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return s.readRows, &pb.PageResult{}, nil
}

func (s *primaryTestStore) ScanRows(context.Context, *pb.PrimaryStoreTarget, pb.DataKind, *pb.VersionRange, pb.SortOrder, []string, *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return s.scanRows, &pb.PageResult{}, nil
}

func (s *primaryTestStore) DeleteRows(context.Context, []*pb.PrimaryStoreKey) error { return nil }

func testOutboxMessage(t *testing.T, nodeID string) []byte {
	t.Helper()
	now := timestamppb.Now()
	msg := &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       "msg-1",
		Topic:           "moox.storage.rows_committed.time_series.v1.mzxw6",
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        &messagepb.Producer{ServiceName: "moox-storage", InstanceId: nodeID},
		Sequence:        1,
		OccurredAt:      now,
		PublishedAt:     now,
		ContentType:     "application/x-protobuf",
		MessageType:     "moox.storage.time_series.rows_committed.v1",
		Payload:         []byte("payload"),
	}
	data, err := proto.Marshal(msg)
	require.NoError(t, err)
	return data
}

func TestDeletePrimaryRowsRequiresKeys(t *testing.T) {
	svc := NewService(Options{Pebble: &deleteTestStore{}})
	defer svc.Close()
	rsp, err := svc.DeletePrimaryRows(context.Background(), &pb.DeletePrimaryRowsReq{Target: &pb.PrimaryStoreTarget{NodeId: "node-1"}})
	if err != nil {
		t.Fatalf("DeletePrimaryRows() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret_info = %+v, want invalid_param", rsp.GetRetInfo())
	}
}

func TestDeletePrimaryRowsDeletesExactKeys(t *testing.T) {
	store := &deleteTestStore{}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()
	keys := []*pb.PrimaryStoreKey{{SpaceId: "moox_system", DatasetId: "host_resource_v1", Key: "agent|1m|_", Version: "2026-07-11T00:00:00.000000000Z"}}
	rsp, err := svc.DeletePrimaryRows(context.Background(), &pb.DeletePrimaryRowsReq{Target: &pb.PrimaryStoreTarget{NodeId: "node-1"}, Keys: keys})
	if err != nil {
		t.Fatalf("DeletePrimaryRows() error = %v", err)
	}
	if rsp.GetDeleted() != 1 || len(store.deleted) != 1 || store.deleted[0].GetVersion() != keys[0].GetVersion() {
		t.Fatalf("deleted=%d keys=%v, want exact key", rsp.GetDeleted(), store.deleted)
	}
}

type deleteTestStore struct {
	deleted []*pb.PrimaryStoreKey
}

func (s *deleteTestStore) Close() error                                           { return nil }
func (s *deleteTestStore) WriteRows(context.Context, []*pb.PrimaryStoreRow) error { return nil }
func (s *deleteTestStore) WriteRowsWithOutbox(context.Context, []*pb.PrimaryStoreRow, *device.OutboxEntry) error {
	return nil
}
func (s *deleteTestStore) ListOutbox(context.Context, uint64, int, int) ([]*device.OutboxEntry, error) {
	return nil, nil
}
func (s *deleteTestStore) DeleteOutbox(context.Context, []uint64) error { return nil }
func (s *deleteTestStore) ReadRows(context.Context, []*pb.PrimaryStoreKey, *pb.VersionRange, pb.SortOrder, []string, *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}
func (s *deleteTestStore) ScanRows(context.Context, *pb.PrimaryStoreTarget, pb.DataKind, *pb.VersionRange, pb.SortOrder, []string, *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}
func (s *deleteTestStore) DeleteRows(_ context.Context, keys []*pb.PrimaryStoreKey) error {
	s.deleted = append(s.deleted, keys...)
	return nil
}

func TestDeletePrimaryRowsDeletesKeys(t *testing.T) {
	store := &primaryTestStore{}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()

	keys := []*pb.PrimaryStoreKey{{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}
	rsp, err := svc.DeletePrimaryRows(context.Background(), &pb.DeletePrimaryRowsReq{
		Target: &pb.PrimaryStoreTarget{NodeId: "node-1"},
		Keys:   keys,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, uint32(1), rsp.GetDeleted())
}

func TestDeletePrimaryRowsRejectsMissingKeys(t *testing.T) {
	svc := NewService(Options{Pebble: &primaryTestStore{}})
	defer svc.Close()
	rsp, err := svc.DeletePrimaryRows(context.Background(), &pb.DeletePrimaryRowsReq{Target: &pb.PrimaryStoreTarget{}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestNormalizeScanPrimaryRowsReqClonesRequest(t *testing.T) {
	req := &pb.ScanPrimaryRowsReq{Target: &pb.PrimaryStoreTarget{SpaceId: "crypto"}}
	normalized := normalizeScanPrimaryRowsReq(req)
	assert.Equal(t, "crypto", normalized.GetTarget().GetSpaceId())
	assert.NotSame(t, req, normalized)
}

func TestNormalizeScanPrimaryRowsReqNilRequest(t *testing.T) {
	normalized := normalizeScanPrimaryRowsReq(nil)
	require.NotNil(t, normalized)
}

func TestValidateOutboxMessageRejectsEmptyPayload(t *testing.T) {
	msg := testOutboxMessage(t, "node-1")
	err := validateOutboxMessage(&pb.WritePrimaryRowsReq{
		Target:        &pb.PrimaryStoreTarget{NodeId: "node-1"},
		OutboxMessage: msg[:len(msg)-1],
	})
	require.Error(t, err)
}

func TestValidateOutboxMessageRejectsMissingTargetNode(t *testing.T) {
	msg := testOutboxMessage(t, "node-1")
	err := validateOutboxMessage(&pb.WritePrimaryRowsReq{OutboxMessage: msg})
	require.Error(t, err)
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

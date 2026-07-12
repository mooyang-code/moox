package primary

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.NotNil(t, store.outboxEntry)
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
		Topic:           "moox.storage.time_series.rows_updated.v1",
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        &messagepb.Producer{ServiceName: "moox-storage", InstanceId: nodeID},
		OccurredAt:      now,
		PublishedAt:     now,
		ContentType:     "application/x-protobuf",
		Payload:         []byte("payload"),
	}
	data, err := proto.Marshal(msg)
	require.NoError(t, err)
	return data
}

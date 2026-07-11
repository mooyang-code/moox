package primary

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

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

package access

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestReadTimeSeriesRowsAppliesGlobalPageWithoutPerKeyTruncation(t *testing.T) {
	primary := &multiKeyPrimary{rowsPerKey: 1500}
	svc := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}

	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{
			{SpaceId: "crypto", DatasetId: "kline", SubjectId: "A", Freq: "1m"},
			{SpaceId: "crypto", DatasetId: "kline", SubjectId: "B", Freq: "1m"},
		},
		Page: &pb.Page{Page: 50, Size: 25},
	})
	if err != nil {
		t.Fatalf("ReadTimeSeriesRows: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %+v", rsp.GetRetInfo())
	}
	if len(primary.pageSizes) != 2 || primary.pageSizes[0] != 1251 || primary.pageSizes[1] != 1251 {
		t.Fatalf("downstream page sizes = %v, want offset + size + 1 for every key", primary.pageSizes)
	}
	if len(rsp.GetRows()) != 25 || rsp.GetRows()[0].GetKey().GetSubjectId() != "A" {
		t.Fatalf("rows = %d first subject = %q, want the 50th global page from subject A", len(rsp.GetRows()), rsp.GetRows()[0].GetKey().GetSubjectId())
	}
	if rsp.GetPageResult().GetTotalState() != pb.TotalState_SKIPPED || !rsp.GetPageResult().GetHasMore() {
		t.Fatalf("page_result = %+v, want skipped total and has_more", rsp.GetPageResult())
	}
}

func TestReadRecordRowsUsesRecordIDForGlobalPage(t *testing.T) {
	primary := &multiKeyPrimary{rowsPerKey: 1500}
	svc := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}

	rsp, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{
		Keys: []*pb.RecordKey{
			{SpaceId: "crypto", DatasetId: "news", RecordId: "A"},
			{SpaceId: "crypto", DatasetId: "news", RecordId: "B"},
		},
		VersionRange: &pb.VersionRange{StartVersion: "2026-07-01T00:00:00Z", EndVersion: "2026-07-02T00:00:00Z"},
		Page:         &pb.Page{Page: 50, Size: 25},
	})
	if err != nil {
		t.Fatalf("ReadRecordRows: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %+v", rsp.GetRetInfo())
	}
	if len(primary.pageSizes) != 2 || primary.pageSizes[0] != 1251 || primary.pageSizes[1] != 1251 {
		t.Fatalf("downstream page sizes = %v, want offset + size + 1 for every key", primary.pageSizes)
	}
	if len(rsp.GetRows()) != 25 || rsp.GetRows()[0].GetKey().GetRecordId() != "A" {
		t.Fatalf("rows = %d first record = %q, want the 50th global page from record A", len(rsp.GetRows()), rsp.GetRows()[0].GetKey().GetRecordId())
	}
	if rsp.GetPageResult().GetTotalState() != pb.TotalState_SKIPPED || !rsp.GetPageResult().GetHasMore() {
		t.Fatalf("page_result = %+v, want skipped total and has_more", rsp.GetPageResult())
	}
}

func TestReadRecordRowsDoesNotCapVersionPrefixKeys(t *testing.T) {
	primary := &multiKeyPrimary{rowsPerKey: 2}
	svc := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}

	rsp, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{
		Keys: []*pb.RecordKey{
			{SpaceId: "crypto", DatasetId: "news", RecordId: "A"},
			{SpaceId: "crypto", DatasetId: "news", RecordId: "B"},
		},
		Page: &pb.Page{Page: 2, Size: 1},
	})
	if err != nil {
		t.Fatalf("ReadRecordRows: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %+v", rsp.GetRetInfo())
	}
	if len(primary.pageSizes) != 2 || primary.pageSizes[0] != 3 || primary.pageSizes[1] != 3 {
		t.Fatalf("downstream page sizes = %v, want page offset + size + 1 for version-prefix keys", primary.pageSizes)
	}
}

func TestReadTimeSeriesRowsAllowsLargeExactKeyBatch(t *testing.T) {
	primary := &multiKeyPrimary{rowsPerKey: 1}
	svc := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}
	keys := make([]*pb.TimeSeriesKey, 0, 200)
	for i := 0; i < 200; i++ {
		keys = append(keys, &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: fmt.Sprintf("subject-%03d", i), Freq: "1m",
			DataTime: "2026-07-10T00:00:00Z",
		})
	}

	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{Keys: keys})
	if err != nil {
		t.Fatalf("ReadTimeSeriesRows: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 200 {
		t.Fatalf("ret=%+v rows=%d, want all exact rows", rsp.GetRetInfo(), len(rsp.GetRows()))
	}
	for _, size := range primary.pageSizes {
		if size != 1 {
			t.Fatalf("downstream exact-key page size = %d, want 1", size)
		}
	}
}

func TestReadRecordRowsAllowsLargeExactKeyBatch(t *testing.T) {
	primary := &multiKeyPrimary{rowsPerKey: 1}
	svc := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}
	keys := make([]*pb.RecordKey, 0, 200)
	for i := 0; i < 200; i++ {
		keys = append(keys, &pb.RecordKey{
			SpaceId: "crypto", DatasetId: "news", RecordId: fmt.Sprintf("record-%03d", i),
			Version: "2026-07-10T00:00:00Z",
		})
	}

	rsp, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{Keys: keys})
	if err != nil {
		t.Fatalf("ReadRecordRows: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 200 {
		t.Fatalf("ret=%+v rows=%d, want all exact rows", rsp.GetRetInfo(), len(rsp.GetRows()))
	}
	for _, size := range primary.pageSizes {
		if size != 1 {
			t.Fatalf("downstream exact-key page size = %d, want 1", size)
		}
	}
}

type multiKeyPrimary struct {
	rowsPerKey int
	pageSizes  []uint32
}

func (*multiKeyPrimary) WriteRows(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow) error {
	return nil
}

func (p *multiKeyPrimary) ReadRows(_ context.Context, _ *pb.PrimaryStoreTarget, req *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	size := uint32(1000)
	if req.GetPage().GetSize() > 0 {
		size = req.GetPage().GetSize()
	}
	p.pageSizes = append(p.pageSizes, size)
	count := min(int(size), p.rowsPerKey)
	rows := make([]*pb.PrimaryStoreRow, 0, count)
	for i := 0; i < count; i++ {
		key := proto.Clone(req.GetKeys()[0]).(*pb.PrimaryStoreKey)
		key.Version = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		rows = append(rows, &pb.PrimaryStoreRow{Key: key, Attributes: map[string]string{"sequence": fmt.Sprint(i)}})
	}
	return rows, &pb.PageResult{Size: size, HasMore: count < p.rowsPerKey, TotalState: pb.TotalState_SKIPPED}, nil
}

func (*multiKeyPrimary) ScanRows(context.Context, *pb.PrimaryStoreTarget, *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (*multiKeyPrimary) ApplyRecordMutations(context.Context, *pb.PrimaryStoreTarget, string, []*pb.RecordMutation) (*pb.RecordRowsCommittedEvent, error) {
	return nil, fmt.Errorf("record mutations not implemented in test primary")
}
func (*multiKeyPrimary) OpenRecordSnapshot(context.Context, *pb.OpenRecordSnapshotReq) (*pb.OpenRecordSnapshotRsp, error) {
	return nil, fmt.Errorf("record snapshots not implemented in test primary")
}
func (*multiKeyPrimary) ReadRecordSnapshot(context.Context, *pb.ReadRecordSnapshotReq) (*pb.ReadRecordSnapshotRsp, error) {
	return nil, fmt.Errorf("record snapshots not implemented in test primary")
}
func (*multiKeyPrimary) ScanRecordSnapshot(context.Context, *pb.ScanRecordSnapshotReq) (*pb.ScanRecordSnapshotRsp, error) {
	return nil, fmt.Errorf("record snapshots not implemented in test primary")
}
func (*multiKeyPrimary) RenewRecordSnapshot(context.Context, *pb.RenewRecordSnapshotReq) error {
	return fmt.Errorf("record snapshots not implemented in test primary")
}
func (*multiKeyPrimary) CloseRecordSnapshot(context.Context, *pb.CloseRecordSnapshotReq) error {
	return fmt.Errorf("record snapshots not implemented in test primary")
}
func (*multiKeyPrimary) GetRecordWatermark(context.Context, *pb.PrimaryStoreTarget) (string, uint64, error) {
	return "", 0, fmt.Errorf("record journal not implemented in test primary")
}
func (*multiKeyPrimary) ScanRecordJournal(context.Context, *pb.ScanRecordJournalReq) (*pb.ScanRecordJournalRsp, error) {
	return nil, fmt.Errorf("record journal not implemented in test primary")
}

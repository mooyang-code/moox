package access

import (
	"context"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	"github.com/mooyang-code/moox/modules/storage/internal/core/schema"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestUpsertRecordRowsReturnsServerManagedRevisionMetadata(t *testing.T) {
	primary := &recordPrimary{event: &pb.RecordRowsCommittedEvent{Rows: []*pb.RecordRow{{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}, Revision: 7, UpdatedAt: "2026-07-11T00:00:00Z", CommitSeq: 11}}, CommitSeq: 11, SourceId: "source-1", EventId: "source-1:11"}}
	service := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{}), validator: schema.NewValidator(recordMetadata{})}
	rsp, err := service.UpsertRecordRows(context.Background(), &pb.UpsertRecordRowsReq{RequestId: "request-1", Mutations: []*pb.RecordMutation{{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || rsp.GetRows()[0].GetRevision() != 7 || rsp.GetRows()[0].GetCommitSeq() != 11 {
		t.Fatalf("response = %+v", rsp)
	}
	if primary.requestID != "request-1" {
		t.Fatalf("request id = %q", primary.requestID)
	}
}

func TestReadRecordRowsDefaultsToCurrent(t *testing.T) {
	primary := &recordPrimary{current: []*pb.RecordRow{{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}, Revision: 2, UpdatedAt: "2026-07-11T00:00:00Z", CommitSeq: 2}}}
	service := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}
	rsp, err := service.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{Keys: []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetRevision() != 2 {
		t.Fatalf("response = %+v", rsp)
	}
}

func TestReadRecordRowsHistoryUsesNumericRevisionRange(t *testing.T) {
	primary := &recordPrimary{current: []*pb.RecordRow{
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}, Revision: 10},
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}, Revision: 2},
	}}
	service := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}
	rsp, err := service.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{Mode: pb.RecordReadMode_RECORD_READ_MODE_HISTORY, RevisionRange: &pb.RevisionRange{StartRevision: 2, EndRevision: 10}, Keys: []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rsp.GetRows()) != 2 || rsp.GetRows()[0].GetRevision() != 2 || rsp.GetRows()[1].GetRevision() != 10 {
		t.Fatalf("history rows = %+v", rsp.GetRows())
	}
}

func TestRecordAccessSnapshotBindsDatasetScope(t *testing.T) {
	primary := &recordPrimary{current: []*pb.RecordRow{{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}}}}
	service := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}
	opened, err := service.OpenRecordAccessSnapshot(context.Background(), &pb.OpenRecordAccessSnapshotReq{Scope: &pb.RecordAccessScope{SpaceId: "crypto", DatasetIds: []string{"news"}}, Mode: pb.RecordReadMode_RECORD_READ_MODE_CURRENT})
	if err != nil || opened.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("open = %+v err=%v", opened, err)
	}
	read, err := service.ReadRecordAccessSnapshot(context.Background(), &pb.ReadRecordAccessSnapshotReq{SnapshotId: opened.GetSnapshotId(), DatasetId: "other", RecordIds: []string{"news-1"}})
	if err != nil || read.GetRetInfo().GetCode() != pb.ErrorCode_NOT_FOUND {
		t.Fatalf("out-of-scope read = %+v err=%v", read, err)
	}
	if _, err := service.CloseRecordAccessSnapshot(context.Background(), &pb.CloseRecordAccessSnapshotReq{SnapshotId: opened.GetSnapshotId()}); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertRecordRowsReturnsSuccessWhenCommittedEventPublishFails(t *testing.T) {
	reported := false
	service := &Service{primary: &recordPrimary{event: &pb.RecordRowsCommittedEvent{Rows: []*pb.RecordRow{{Revision: 1}}}}, router: router.NewResolver(fakeRouteReader{}), events: failingCommittedPublisher{}, report: func(context.Context, string, error) { reported = true }}
	rsp, err := service.UpsertRecordRows(context.Background(), &pb.UpsertRecordRowsReq{RequestId: "request-1", Mutations: []*pb.RecordMutation{{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}}}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || !reported {
		t.Fatalf("response=%+v err=%v reported=%v", rsp, err, reported)
	}
}

func TestUpsertRecordRowsNeverAcceptsCallerRevisionMetadata(t *testing.T) {
	service := &Service{primary: &recordPrimary{event: &pb.RecordRowsCommittedEvent{}}, router: router.NewResolver(fakeRouteReader{}), validator: schema.NewValidator(recordMetadata{})}
	rsp, err := service.UpsertRecordRows(context.Background(), &pb.UpsertRecordRowsReq{RequestId: "request-1", Mutations: []*pb.RecordMutation{{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRetInfo().GetCode() == pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("response = %+v", rsp)
	}
}

func TestUpsertRecordRowsSurfacesExpectedRevisionConflict(t *testing.T) {
	service := &Service{primary: &recordPrimary{applyErr: fmt.Errorf("revision conflict: expected 1, got 2")}, router: router.NewResolver(fakeRouteReader{})}
	rsp, err := service.UpsertRecordRows(context.Background(), &pb.UpsertRecordRowsReq{RequestId: "request-1", Mutations: []*pb.RecordMutation{{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_REVISION_CONFLICT {
		t.Fatalf("response = %+v", rsp)
	}
}

type recordPrimary struct {
	event     *pb.RecordRowsCommittedEvent
	current   []*pb.RecordRow
	requestID string
	applyErr  error
}

func (p *recordPrimary) WriteRows(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow) error {
	return nil
}
func (p *recordPrimary) ReadRows(context.Context, *pb.PrimaryStoreTarget, *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}
func (p *recordPrimary) ScanRows(context.Context, *pb.PrimaryStoreTarget, *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}
func (p *recordPrimary) ApplyRecordMutations(_ context.Context, _ *pb.PrimaryStoreTarget, requestID string, _ []*pb.RecordMutation) (*pb.RecordRowsCommittedEvent, error) {
	p.requestID = requestID
	if p.applyErr != nil {
		return nil, p.applyErr
	}
	return p.event, nil
}
func (p *recordPrimary) OpenRecordSnapshot(context.Context, *pb.OpenRecordSnapshotReq) (*pb.OpenRecordSnapshotRsp, error) {
	return &pb.OpenRecordSnapshotRsp{SnapshotId: "snapshot-1"}, nil
}
func (p *recordPrimary) ReadRecordSnapshot(context.Context, *pb.ReadRecordSnapshotReq) (*pb.ReadRecordSnapshotRsp, error) {
	return &pb.ReadRecordSnapshotRsp{Rows: p.current}, nil
}
func (p *recordPrimary) ScanRecordSnapshot(context.Context, *pb.ScanRecordSnapshotReq) (*pb.ScanRecordSnapshotRsp, error) {
	return &pb.ScanRecordSnapshotRsp{Rows: p.current, PageResult: &pb.PageResult{}}, nil
}
func (p *recordPrimary) RenewRecordSnapshot(context.Context, *pb.RenewRecordSnapshotReq) error {
	return nil
}
func (p *recordPrimary) CloseRecordSnapshot(context.Context, *pb.CloseRecordSnapshotReq) error {
	return nil
}
func (p *recordPrimary) GetRecordWatermark(context.Context, *pb.PrimaryStoreTarget) (string, uint64, error) {
	return "source-1", 1, nil
}
func (p *recordPrimary) ScanRecordJournal(context.Context, *pb.ScanRecordJournalReq) (*pb.ScanRecordJournalRsp, error) {
	return &pb.ScanRecordJournalRsp{PageResult: &pb.PageResult{}}, nil
}

type recordMetadata struct{}

func (recordMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return &pb.Dataset{DatasetId: "news", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}, nil
}

type failingCommittedPublisher struct{}

func (failingCommittedPublisher) PublishTimeSeriesRowsChanged(context.Context, *pb.TimeSeriesRowsChangedEvent) error {
	return nil
}
func (failingCommittedPublisher) PublishRecordRowsCommitted(context.Context, *pb.RecordRowsCommittedEvent) error {
	return fmt.Errorf("publish failed")
}
func (recordMetadata) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

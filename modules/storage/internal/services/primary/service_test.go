package primary

import (
	"context"
	"path/filepath"
	"testing"

	devicepebble "github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestPrimaryServiceAppliesRecordMutationAndReturnsCommittedRows(t *testing.T) {
	store, err := devicepebble.Open(devicepebble.Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(Options{Pebble: store})
	req := &pb.ApplyPrimaryRecordMutationsReq{
		SourceTarget: &pb.PrimaryStoreTarget{Engine: "pebble", SpaceId: "crypto", DatasetId: "symbols"},
		RequestId:    "request-1",
		Mutations:    []*pb.RecordMutation{{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "BTC-USDT"}}},
	}
	rsp, err := service.ApplyPrimaryRecordMutations(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || rsp.GetCommit().GetCommitSeq() != 1 || rsp.GetCommit().GetRows()[0].GetRevision() != 1 {
		t.Fatalf("response = %+v", rsp)
	}
}

func TestPrimaryServiceRecordSnapshotAndJournalTransport(t *testing.T) {
	store, err := devicepebble.Open(devicepebble.Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewService(Options{Pebble: store})
	target := &pb.PrimaryStoreTarget{Engine: "pebble", SpaceId: "crypto", DatasetId: "symbols"}
	commit, err := service.ApplyPrimaryRecordMutations(context.Background(), &pb.ApplyPrimaryRecordMutationsReq{SourceTarget: target, RequestId: "request-1", Mutations: []*pb.RecordMutation{{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "BTC-USDT"}}}})
	if err != nil || commit.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("commit = %+v err=%v", commit, err)
	}
	snapshot, err := service.OpenRecordSnapshot(context.Background(), &pb.OpenRecordSnapshotReq{SourceTarget: target, Mode: pb.RecordReadMode_RECORD_READ_MODE_CURRENT})
	if err != nil || snapshot.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || snapshot.GetSnapshotId() == "" {
		t.Fatalf("snapshot = %+v err=%v", snapshot, err)
	}
	journal, err := service.ScanRecordJournal(context.Background(), &pb.ScanRecordJournalReq{Target: target, ThroughCommitSeq: 1, Page: &pb.Page{Size: 10}})
	if err != nil || journal.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(journal.GetEvents()) != 1 || journal.GetScannedThroughCommitSeq() != 1 {
		t.Fatalf("journal = %+v err=%v", journal, err)
	}
	if _, err := service.CloseRecordSnapshot(context.Background(), &pb.CloseRecordSnapshotReq{SnapshotId: snapshot.GetSnapshotId()}); err != nil {
		t.Fatal(err)
	}
}

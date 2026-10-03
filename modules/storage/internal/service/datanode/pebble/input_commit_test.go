package pebble

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

func TestInputCommitIncompleteBaseFieldsDoNotBecomeReady(t *testing.T) {
	store := openInputCommitStore(t)
	key := inputCommitKey("BTC-USDT")
	_, err := store.CommitInput(context.Background(), InputCommit{
		CommitID:       "commit-incomplete",
		RequiredFields: []string{"open", "close"},
		Row:            &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("open", 1)}},
		WriteKind:      WriteKindInputCommit,
	})
	if err == nil {
		t.Fatal("incomplete base fields must be rejected")
	}
	got, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"open", "close"}, []string{attrInputReady})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows=%v", got)
	}
	if inputReady(got[0]) {
		t.Fatal("incomplete commit must not set input_ready")
	}
}

func TestInputCommitIdempotentRetryReturnsOriginalReceipt(t *testing.T) {
	store := openInputCommitStore(t)
	key := inputCommitKey("ETH-USDT")
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("open", 2), doubleField("close", 3)}}
	first, err := store.CommitInput(context.Background(), InputCommit{
		CommitID: "commit-retry", RequiredFields: []string{"open", "close"}, Row: row, WriteKind: WriteKindInputCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.CommitID != "commit-retry" || first.Position.Sequence == 0 || !first.InputReady {
		t.Fatalf("first receipt=%v", first)
	}
	second, err := store.CommitInput(context.Background(), InputCommit{
		CommitID: "commit-retry", RequiredFields: []string{"open", "close"}, Row: proto.Clone(row).(*pb.RowFieldUpsert), WriteKind: WriteKindInputCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Position != first.Position || second.CommitID != first.CommitID {
		t.Fatalf("retry receipt=%v want %v", second, first)
	}
	lookup, err := store.LookupWriteReceipt(context.Background(), "commit-retry")
	if err != nil {
		t.Fatal(err)
	}
	if lookup.Position != first.Position {
		t.Fatalf("lookup receipt=%v want %v", lookup, first)
	}
}

func TestInputCommitRetryDoesNotChangeCommittedBase(t *testing.T) {
	store := openInputCommitStore(t)
	key := inputCommitKey("SOL-USDT")
	_, err := store.CommitInput(context.Background(), InputCommit{
		CommitID: "commit-stable", RequiredFields: []string{"close"},
		Row:       &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("close", 10)}},
		WriteKind: WriteKindInputCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitInput(context.Background(), InputCommit{
		CommitID: "commit-stable", RequiredFields: []string{"close"},
		Row:       &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("close", 99)}},
		WriteKind: WriteKindInputCommit,
	})
	if err == nil {
		t.Fatal("conflicting retry must not rewrite committed base fields")
	}
	got, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close"}, nil)
	if err != nil || len(got) != 1 || got[0].GetFields()[0].GetValue().GetDoubleValue() != 10 {
		t.Fatalf("base fields changed: %v err=%v", got, err)
	}
}

func openInputCommitStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func inputCommitKey(subject string) *pb.RowKey {
	return &pb.RowKey{SpaceId: "space", DatasetId: "mdataset_crypto_kline_1m", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: subject, Freq: "1m", DataTime: "2026-09-13T16:00:00Z",
	}}}
}

func doubleField(id string, value float64) *pb.FieldValue {
	return &pb.FieldValue{FieldId: id, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func inputReady(row *pb.RowFieldValues) bool {
	if row == nil {
		return false
	}
	value, ok := row.GetAttributes()[attrInputReady]
	return ok && value.GetBoolValue()
}

func TestInputCommitEventWriteKindIsDerivedFromOperation(t *testing.T) {
	store := openInputCommitStore(t)
	key := inputCommitKey("ADA-USDT")
	_, err := store.CommitInput(context.Background(), InputCommit{
		CommitID: "commit-kind", RequiredFields: []string{"close"},
		Row:       &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("close", 1)}},
		WriteKind: WriteKindInputCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox=%v err=%v", entries, err)
	}
	if got := outboxWriteKind(t, entries[0].Data); got != WriteKindInputCommit {
		t.Fatalf("commit write_kind=%q", got)
	}
}

func outboxWriteKind(t *testing.T, data []byte) string {
	t.Helper()
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		t.Fatal(err)
	}
	payload := &storagepb.DatasetRowsUpserted{}
	if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
		t.Fatal(err)
	}
	return payload.GetWriteKind()
}

package pebble

import (
	"context"
	"path/filepath"
	"sync"
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
		CommitID:        "commit-incomplete",
		RequiredFields:  []string{"open", "close"},
		Row:             &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("open", 1)}},
		WriteKind:       WriteKindInputCommit,
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
		Row: &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("close", 10)}},
		WriteKind: WriteKindInputCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitInput(context.Background(), InputCommit{
		CommitID: "commit-stable", RequiredFields: []string{"close"},
		Row: &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("close", 99)}},
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

func TestInputCommitConcurrentFactorPatchesDoNotClobber(t *testing.T) {
	store := openInputCommitStore(t)
	key := inputCommitKey("BNB-USDT")
	_, err := store.CommitInput(context.Background(), InputCommit{
		CommitID: "commit-base", RequiredFields: []string{"close"},
		Row: &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("close", 4)}},
		WriteKind: WriteKindInputCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := store.PatchFactor(context.Background(), FactorPatch{
			CommitID: "patch-ma", BindingVersion: "bind-ma-1", OwnedFields: []string{"ma"},
			Row: &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("ma", 4.1)}},
		})
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := store.PatchFactor(context.Background(), FactorPatch{
			CommitID: "patch-vol", BindingVersion: "bind-vol-1", OwnedFields: []string{"vol"},
			Row: &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("vol", 100)}},
		})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close", "ma", "vol"}, []string{attrInputReady})
	if err != nil || len(got) != 1 {
		t.Fatalf("rows=%v err=%v", got, err)
	}
	values := fieldMap(got[0])
	if values["close"] != 4 || values["ma"] != 4.1 || values["vol"] != 100 {
		t.Fatalf("patched fields=%v", values)
	}
	if !inputReady(got[0]) {
		t.Fatal("factor patch must not clear input_ready")
	}
}

func TestInputCommitFactorPatchCannotWriteBaseFields(t *testing.T) {
	store := openInputCommitStore(t)
	key := inputCommitKey("XRP-USDT")
	_, err := store.CommitInput(context.Background(), InputCommit{
		CommitID: "commit-xrp", RequiredFields: []string{"close"},
		Row: &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("close", 1)}},
		WriteKind: WriteKindInputCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PatchFactor(context.Background(), FactorPatch{
		CommitID: "patch-evil", BindingVersion: "bind-ma-1", OwnedFields: []string{"ma"},
		Row: &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("close", 0), doubleField("ma", 1.1)}},
	})
	if err == nil {
		t.Fatal("factor patch must not write unowned base fields")
	}
	got, err := store.ReadFields(context.Background(), []*pb.RowKey{key}, []string{"close", "ma"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	values := fieldMap(got[0])
	if values["close"] != 1 {
		t.Fatalf("base field overwritten: %v", values)
	}
	if _, ok := values["ma"]; ok {
		t.Fatalf("rejected patch still wrote owned field: %v", values)
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

func fieldMap(row *pb.RowFieldValues) map[string]float64 {
	out := map[string]float64{}
	if row == nil {
		return out
	}
	for _, field := range row.GetFields() {
		out[field.GetFieldId()] = field.GetValue().GetDoubleValue()
	}
	return out
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
		Row: &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("close", 1)}},
		WriteKind: WriteKindInputCommit,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PatchFactor(context.Background(), FactorPatch{
		CommitID: "patch-kind", BindingVersion: "bind-1", OwnedFields: []string{"ma"},
		Row: &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{doubleField("ma", 1.2)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("outbox=%v err=%v", entries, err)
	}
	if got := outboxWriteKind(t, entries[0].Data); got != WriteKindInputCommit {
		t.Fatalf("commit write_kind=%q", got)
	}
	if got := outboxWriteKind(t, entries[1].Data); got != WriteKindFactorPatch {
		t.Fatalf("patch write_kind=%q", got)
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

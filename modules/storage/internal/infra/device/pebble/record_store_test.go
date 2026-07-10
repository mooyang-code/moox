package pebble

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestApplyRecordMutationCreatesImmutableRevisionAndCurrentSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	first, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{
		testRecordMutation("request-1", "BTC-USDT", "first"),
	})
	if err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	if len(first.GetRows()) != 1 || first.GetRows()[0].GetRevision() != 1 {
		t.Fatalf("first commit = %+v, want revision 1", first)
	}

	secondMutation := testRecordMutation("request-2", "BTC-USDT", "second")
	secondMutation.Columns = nil
	secondMutation.Attributes = map[string]string{"source": "test"}
	second, err := store.ApplyRecordMutations(ctx, "request-2", []*pb.RecordMutation{secondMutation})
	if err != nil {
		t.Fatalf("second mutation: %v", err)
	}
	if len(second.GetRows()) != 1 || second.GetRows()[0].GetRevision() != 2 {
		t.Fatalf("second commit = %+v, want revision 2", second)
	}

	history, err := store.readRecordHistory(ctx, &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "BTC-USDT"}, nil, nil)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(history) != 2 || history[0].GetRevision() != 1 || history[1].GetRevision() != 2 {
		t.Fatalf("history revisions = %v, want [1 2]", recordRevisions(history))
	}
	if history[0].GetAttributes()["source"] != "" {
		t.Fatalf("revision 1 mutated in place: %+v", history[0].GetAttributes())
	}
	if history[1].GetAttributes()["source"] != "test" {
		t.Fatalf("revision 2 attributes = %+v", history[1].GetAttributes())
	}
}

func TestApplyRecordMutationEnforcesExpectedRevision(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if _, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "first")}); err != nil {
		t.Fatalf("seed mutation: %v", err)
	}
	mutation := testRecordMutation("request-2", "BTC-USDT", "stale")
	mutation.ExpectedRevision = ptrUint64(0)
	if _, err := store.ApplyRecordMutations(ctx, "request-2", []*pb.RecordMutation{mutation}); err == nil {
		t.Fatal("expected revision conflict")
	}
	_, watermark, err := store.RecordWatermark(ctx)
	if err != nil {
		t.Fatalf("RecordWatermark: %v", err)
	}
	if watermark != 1 {
		t.Fatalf("watermark = %d, want 1 after conflict", watermark)
	}
}

func TestApplyRecordMutationRetryReturnsOriginalCommitWithoutNewRevision(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	mutation := testRecordMutation("request-1", "BTC-USDT", "first")
	first, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{mutation})
	if err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	retry, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{mutation})
	if err != nil {
		t.Fatalf("retry mutation: %v", err)
	}
	if retry.GetCommitSeq() != first.GetCommitSeq() || retry.GetRows()[0].GetRevision() != 1 {
		t.Fatalf("retry = %+v, first = %+v", retry, first)
	}
	_, watermark, err := store.RecordWatermark(ctx)
	if err != nil {
		t.Fatalf("RecordWatermark: %v", err)
	}
	if watermark != 1 {
		t.Fatalf("watermark = %d, want 1 after idempotent retry", watermark)
	}
}

func TestApplyRecordMutationMergesPatchIntoCompleteRevision(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	first := testRecordMutation("request-1", "BTC-USDT", "first")
	first.Columns = append(first.Columns, testColumn("price", "100"))
	if _, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{first}); err != nil {
		t.Fatal(err)
	}
	second := testRecordMutation("request-2", "BTC-USDT", "second")
	second.Columns = second.Columns[:1]
	event, err := store.ApplyRecordMutations(ctx, "request-2", []*pb.RecordMutation{second})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(event.GetRows()[0].GetColumns()); got != 2 {
		t.Fatalf("merged columns = %d, want 2", got)
	}
	if got := event.GetRows()[0].GetColumns()[1].GetValue().GetStringValue(); got != "100" {
		t.Fatalf("preserved column = %q, want 100", got)
	}
}

func TestApplyRecordMutationMergesAttributesByKey(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	first := testRecordMutation("request-1", "BTC-USDT", "first")
	first.Attributes = map[string]string{"a": "1", "b": "1"}
	if _, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{first}); err != nil {
		t.Fatal(err)
	}
	second := testRecordMutation("request-2", "BTC-USDT", "second")
	second.Attributes = map[string]string{"b": "2", "c": "3"}
	event, err := store.ApplyRecordMutations(ctx, "request-2", []*pb.RecordMutation{second})
	if err != nil {
		t.Fatal(err)
	}
	attrs := event.GetRows()[0].GetAttributes()
	if attrs["a"] != "1" || attrs["b"] != "2" || attrs["c"] != "3" {
		t.Fatalf("attributes = %+v", attrs)
	}
}

func TestApplyRecordMutationRejectsDuplicateIdentityInBatch(t *testing.T) {
	store := openRecordTestStore(t)
	mutation := testRecordMutation("request-1", "BTC-USDT", "first")
	if _, err := store.ApplyRecordMutations(context.Background(), "request-1", []*pb.RecordMutation{mutation, protoCloneMutation(mutation)}); err == nil {
		t.Fatal("expected duplicate identity error")
	}
}

func TestApplyRecordMutationRejectsRequestIDPayloadMismatch(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	if _, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "first")}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "changed")}); err == nil {
		t.Fatal("expected request payload mismatch")
	}
}

func TestConcurrentRecordMutationsProduceContinuousRevisions(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	const count = 20
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.ApplyRecordMutations(ctx, fmt.Sprintf("request-%d", i), []*pb.RecordMutation{testRecordMutation(fmt.Sprintf("request-%d", i), "BTC-USDT", fmt.Sprintf("%d", i))})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.readRecordHistory(ctx, &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "BTC-USDT"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != count {
		t.Fatalf("history length = %d, want %d", len(history), count)
	}
	for index, row := range history {
		if row.GetRevision() != uint64(index+1) {
			t.Fatalf("revision %d = %d", index, row.GetRevision())
		}
	}
}

func TestRecordCommitSequenceSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary")
	store, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ApplyRecordMutations(context.Background(), "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "first")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	second, err := store.ApplyRecordMutations(context.Background(), "request-2", []*pb.RecordMutation{testRecordMutation("request-2", "BTC-USDT", "second")})
	if err != nil {
		t.Fatal(err)
	}
	if second.GetCommitSeq() != first.GetCommitSeq()+1 || second.GetEventId() == first.GetEventId() {
		t.Fatalf("commits = %d/%d events = %q/%q", first.GetCommitSeq(), second.GetCommitSeq(), first.GetEventId(), second.GetEventId())
	}
}

func TestRecordSourceIDAndEventIDSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary")
	store, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ApplyRecordMutations(context.Background(), "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "first")})
	if err != nil {
		t.Fatal(err)
	}
	sourceBefore, _, err := store.RecordWatermark(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sourceAfter, _, err := store.RecordWatermark(context.Background())
	if err != nil || sourceAfter != sourceBefore {
		t.Fatalf("source changed across reopen: before=%q after=%q err=%v", sourceBefore, sourceAfter, err)
	}
	if !strings.HasPrefix(first.GetEventId(), sourceAfter+":") {
		t.Fatalf("event id %q is not source bound", first.GetEventId())
	}
}

func TestRecordMutationAndJournalCommitAtomically(t *testing.T) {
	store := openRecordTestStore(t)
	ctx := context.Background()
	event, err := store.ApplyRecordMutations(ctx, "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "first")})
	if err != nil {
		t.Fatal(err)
	}
	events, scanned, _, err := store.ScanRecordJournal(ctx, 0, event.GetCommitSeq(), &pb.Page{Size: 10})
	if err != nil || scanned != event.GetCommitSeq() || len(events) != 1 || events[0].GetEventId() != event.GetEventId() {
		t.Fatalf("journal events=%v scanned=%d err=%v", events, scanned, err)
	}
	history, err := store.readRecordHistory(ctx, event.GetRows()[0].GetKey(), nil, nil)
	if err != nil || len(history) != 1 || history[0].GetCommitSeq() != event.GetCommitSeq() {
		t.Fatalf("history=%v err=%v", history, err)
	}
}

func TestRecordTimeIndexOrdersWholeAndFractionalSeconds(t *testing.T) {
	first := &pb.RecordRow{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "A"}, UpdatedAt: "2026-07-11T00:00:00Z", Revision: 1}
	second := &pb.RecordRow{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "B"}, UpdatedAt: "2026-07-11T00:00:00.000000001Z", Revision: 1}
	if bytes.Compare(encodeRecordTimeKey(first), encodeRecordTimeKey(second)) >= 0 {
		t.Fatalf("time index keys are not ordered: %q >= %q", encodeRecordTimeKey(first), encodeRecordTimeKey(second))
	}
}

func TestRecordBatchCommitFailureLeavesEveryKeyspaceUnchangedAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary")
	store, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyRecordMutations(context.Background(), "request-1", []*pb.RecordMutation{testRecordMutation("request-1", "BTC-USDT", "first")}); err != nil {
		t.Fatal(err)
	}
	store.commitHook = func() error { return errors.New("injected commit failure") }
	if _, err := store.ApplyRecordMutations(context.Background(), "request-2", []*pb.RecordMutation{testRecordMutation("request-2", "BTC-USDT", "second")}); err == nil {
		t.Fatal("expected injected failure")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, watermark, err := store.RecordWatermark(context.Background())
	if err != nil || watermark != 1 {
		t.Fatalf("watermark after failed commit = %d, err=%v", watermark, err)
	}
	history, err := store.readRecordHistory(context.Background(), &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: "BTC-USDT"}, nil, nil)
	if err != nil || len(history) != 1 || history[0].GetRevision() != 1 {
		t.Fatalf("history after failed commit = %+v, err=%v", history, err)
	}
}

func openRecordTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func protoCloneMutation(mutation *pb.RecordMutation) *pb.RecordMutation {
	clone := *mutation
	clone.Key = &pb.RecordKey{SpaceId: mutation.GetKey().GetSpaceId(), DatasetId: mutation.GetKey().GetDatasetId(), RecordId: mutation.GetKey().GetRecordId()}
	return &clone
}

func testColumn(name, value string) *pb.ColumnValue {
	return &pb.ColumnValue{ColumnName: name, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}}
}

func testRecordMutation(requestID, recordID, note string) *pb.RecordMutation {
	return &pb.RecordMutation{
		Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "symbols", RecordId: recordID},
		Columns: []*pb.ColumnValue{{
			ColumnName: "note",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: note}},
		}},
		Attributes: map[string]string{"request_id": requestID},
	}
}

func ptrUint64(value uint64) *uint64 { return &value }

func recordRevisions(rows []*pb.RecordRow) []uint64 {
	out := make([]uint64, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.GetRevision())
	}
	return out
}

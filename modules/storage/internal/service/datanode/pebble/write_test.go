package pebble

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUpsertFieldsWholeRowUpsertOverwritesNullFields(t *testing.T) {
	store := openFactorRowsStore(t)
	key := factorRowsKey("row-null")
	if _, err := store.UpsertFields(context.Background(), "space", "factor_result", "commit-before", []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{doubleField("close", 10), doubleField("ma", 2)}}}); err != nil {
		t.Fatal(err)
	}
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{
		doubleField("close", 11),
		{FieldId: "ma", Value: &pb.TypedValue{Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE_NULL}}},
	}}
	if _, err := store.UpsertFields(context.Background(), "space", "factor_result", "commit-null", []*pb.RowFieldUpsert{row}); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadRows(context.Background(), []*pb.RowKey{key}, []string{"close", "ma"}, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("rows=%v err=%v", got, err)
	}
	fields := make(map[string]*pb.TypedValue)
	for _, field := range got[0].GetFields() {
		fields[field.GetFieldId()] = field.GetValue()
	}
	if fields["close"].GetDoubleValue() != 11 {
		t.Fatalf("close=%v", fields["close"])
	}
	if fields["ma"] == nil || fields["ma"].GetNullValue() != pb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("ma was not overwritten with explicit null: %v", fields["ma"])
	}
}

func TestUpsertFieldsSameCommitIDIsIdempotent(t *testing.T) {
	store := openFactorRowsStore(t)
	rows := []*pb.RowFieldUpsert{
		{Key: factorRowsKey("row-a"), Fields: []*pb.FieldValue{doubleField("close", 10), doubleField("volume", 2)}},
		{Key: factorRowsKey("row-b"), Fields: []*pb.FieldValue{doubleField("close", 11)}},
	}
	if _, err := store.UpsertFields(context.Background(), "space", "factor_result", "commit-factor", rows); err != nil {
		t.Fatal(err)
	}
	reordered := []*pb.RowFieldUpsert{
		{Key: factorRowsKey("row-b"), Fields: []*pb.FieldValue{doubleField("close", 11)}},
		{Key: factorRowsKey("row-a"), Fields: []*pb.FieldValue{doubleField("volume", 2), doubleField("close", 10)}},
	}
	if _, err := store.UpsertFields(context.Background(), "space", "factor_result", "commit-factor", reordered); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox entries=%v err=%v", entries, err)
	}
}

// 准备在 outboxMu 之外：同一 commitID 并发写入只提交一次，其余按回执判为成功；不同 commitID 并发写入各占一个 outbox 位置。
func TestUpsertFieldsConcurrentWritesCommitOncePerCommitID(t *testing.T) {
	store := openFactorRowsStore(t)
	rows := func(value float64) []*pb.RowFieldUpsert {
		return []*pb.RowFieldUpsert{{Key: factorRowsKey("row-concurrent"), Fields: []*pb.FieldValue{doubleField("close", value)}}}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		commitID, value := "commit-same", 10.0
		if i%2 == 1 {
			commitID, value = fmt.Sprintf("commit-%d", i), float64(i)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.UpsertFields(context.Background(), "space", "factor_result", commitID, rows(value))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.ListOutbox(context.Background(), 0, 100)
	if err != nil || len(entries) != 9 {
		t.Fatalf("outbox entries=%d err=%v", len(entries), err)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].ID != entries[i-1].ID+1 {
			t.Fatalf("outbox ids not contiguous: %d then %d", entries[i-1].ID, entries[i].ID)
		}
	}
	if _, err := store.UpsertFields(context.Background(), "space", "factor_result", "commit-same", rows(99)); !errors.As(err, new(CommitConflictError)) {
		t.Fatalf("same commit id with different rows: err=%v", err)
	}
}

func TestUpsertFieldsThenMarkerOrdering(t *testing.T) {
	store := openFactorRowsStore(t)
	if _, err := store.UpsertFields(context.Background(), "space", "factor_result", "commit-order", []*pb.RowFieldUpsert{{Key: factorRowsKey("row-order"), Fields: []*pb.FieldValue{doubleField("close", 10)}}}); err != nil {
		t.Fatal(err)
	}
	marker := &pb.DatasetPeriodCompletedMarker{
		DatasetId: "factor_result", Frequency: "1m", PeriodTime: 1,
		Status: "complete", CauseEventId: "trigger-1", CompletedAt: timestamppb.Now(),
	}
	message, _, err := BuildProducerPeriodCompletedMessage("space", marker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendDatasetMarker(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("outbox entries=%v err=%v", entries, err)
	}
	if entries[0].ID >= entries[1].ID {
		t.Fatalf("row event id=%d marker id=%d", entries[0].ID, entries[1].ID)
	}
	first, second := &eventpb.EventMessage{}, &eventpb.EventMessage{}
	if err := proto.Unmarshal(entries[0].Data, first); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(entries[1].Data, second); err != nil {
		t.Fatal(err)
	}
	if first.GetEventName() != events.DatasetRowsUpserted.Name() || second.GetEventName() != events.DatasetPeriodCompleted.Name() {
		t.Fatalf("event order=%q, %q", first.GetEventName(), second.GetEventName())
	}
}

func openFactorRowsStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func doubleField(id string, value float64) *pb.FieldValue {
	return &pb.FieldValue{FieldId: id, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func factorRowsKey(recordID string) *pb.RowKey {
	return &pb.RowKey{SpaceId: "space", DatasetId: "factor_result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: recordID, Version: "v1"}}}
}

// commit 写入要求行键唯一（内容摘要按行键规范排序）；不带 commit_id 的写入保持后写覆盖先写。
func TestUpsertFieldsCommitRejectsDuplicateRowKeys(t *testing.T) {
	store := openFactorRowsStore(t)
	rows := []*pb.RowFieldUpsert{
		{Key: factorRowsKey("row-dup"), Fields: []*pb.FieldValue{doubleField("close", 1)}},
		{Key: factorRowsKey("row-dup"), Fields: []*pb.FieldValue{doubleField("close", 2)}},
	}
	if _, err := store.UpsertFields(context.Background(), "space", "factor_result", "commit-dup", rows); err == nil {
		t.Fatal("commit write with duplicate row keys: err = nil")
	}
	if _, err := store.UpsertFields(context.Background(), "space", "factor_result", "", rows); err != nil {
		t.Fatalf("plain write with duplicate row keys: %v", err)
	}
}

// 行的数据集必须与请求一致：一次写入只产生一个数据集的一条事件。
func TestUpsertFieldsRejectsRowsOfAnotherDataset(t *testing.T) {
	store := openFactorRowsStore(t)
	row := &pb.RowFieldUpsert{Key: factorRowsKey("row-a"), Fields: []*pb.FieldValue{doubleField("close", 1)}}
	if _, err := store.UpsertFields(context.Background(), "space", "another", "", []*pb.RowFieldUpsert{row}); err == nil {
		t.Fatal("row of another dataset: err = nil")
	}
	if entries, err := store.ListOutbox(context.Background(), 0, 10); err != nil || len(entries) != 0 {
		t.Fatalf("outbox entries=%d err=%v", len(entries), err)
	}
}

// commit 标记持久化：重开 Pebble 后同一 commit_id 仍判重（内容相同成功且不写入，内容不同冲突）。
func TestUpsertFieldsCommitMarkerSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	rows := func(value float64) []*pb.RowFieldUpsert {
		return []*pb.RowFieldUpsert{{Key: factorRowsKey("row-reopen"), Fields: []*pb.FieldValue{doubleField("close", value)}}}
	}
	store, err := Open(Options{Path: path, NodeID: "node-owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertFields(context.Background(), "space", "factor_result", "commit-reopen", rows(1)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(Options{Path: path, NodeID: "node-owner"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if entries, err := store.UpsertFields(context.Background(), "space", "factor_result", "commit-reopen", rows(1)); err != nil || len(entries) != 0 {
		t.Fatalf("same commit after reopen entries=%d err=%v", len(entries), err)
	}
	if _, err := store.UpsertFields(context.Background(), "space", "factor_result", "commit-reopen", rows(2)); !errors.As(err, new(CommitConflictError)) {
		t.Fatalf("different content after reopen err=%v", err)
	}
}

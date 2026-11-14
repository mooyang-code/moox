package pebble

import (
	"context"
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
	if err := store.UpsertFields(context.Background(), "space", "factor_result", []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{doubleField("close", 10), doubleField("ma", 2)}}}); err != nil {
		t.Fatal(err)
	}
	row := &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{
		doubleField("close", 11),
		{FieldId: "ma", Value: &pb.TypedValue{Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE_NULL}}},
	}}
	if err := store.UpsertFields(context.Background(), "space", "factor_result", []*pb.RowFieldUpsert{row}); err != nil {
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

// 准备在 outboxMu 之外：并发写入各占一个变更序号，序号连续。
func TestUpsertFieldsConcurrentWritesKeepChangeSequenceContiguous(t *testing.T) {
	store := openFactorRowsStore(t)
	rows := func(value float64) []*pb.RowFieldUpsert {
		return []*pb.RowFieldUpsert{{Key: factorRowsKey("row-concurrent"), Fields: []*pb.FieldValue{doubleField("close", value)}}}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		value := float64(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := store.UpsertFields(context.Background(), "space", "factor_result", rows(value))
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
	changes := readAllChanges(t, store, "space", "factor_result")
	if len(changes) != 16 {
		t.Fatalf("changes=%d, want 16", len(changes))
	}
	for i := 1; i < len(changes); i++ {
		if changes[i].GetSequence() != changes[i-1].GetSequence()+1 {
			t.Fatalf("变更序号不连续：%d 之后是 %d", changes[i-1].GetSequence(), changes[i].GetSequence())
		}
	}
}

func TestUpsertFieldsThenMarkerOrdering(t *testing.T) {
	store := openFactorRowsStore(t)
	if err := store.UpsertFields(context.Background(), "space", "factor_result", []*pb.RowFieldUpsert{{Key: factorRowsKey("row-order"), Fields: []*pb.FieldValue{doubleField("close", 10)}}}); err != nil {
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
	// 变更日志里行在前、完成标记在后；Outbox 只有完成标记。
	changes := readAllChanges(t, store, "space", "factor_result")
	if len(changes) != 2 || len(changes[0].GetRecords()) == 0 || len(changes[1].GetMarker()) == 0 {
		t.Fatalf("changes=%v", changes)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox entries=%v err=%v", entries, err)
	}
	markerMessage := &eventpb.EventMessage{}
	if err := proto.Unmarshal(entries[0].Data, markerMessage); err != nil {
		t.Fatal(err)
	}
	if markerMessage.GetEventName() != events.DatasetPeriodCompleted.Name() {
		t.Fatalf("outbox event=%q", markerMessage.GetEventName())
	}
}

func openFactorRowsStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db")})
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

// 一次写入里行键重复时后写覆盖先写。
func TestUpsertFieldsDuplicateRowKeysLastWins(t *testing.T) {
	store := openFactorRowsStore(t)
	rows := []*pb.RowFieldUpsert{
		{Key: factorRowsKey("row-dup"), Fields: []*pb.FieldValue{doubleField("close", 1)}},
		{Key: factorRowsKey("row-dup"), Fields: []*pb.FieldValue{doubleField("close", 2)}},
	}
	if err := store.UpsertFields(context.Background(), "space", "factor_result", rows); err != nil {
		t.Fatalf("write with duplicate row keys: %v", err)
	}
	got, err := store.ReadRows(context.Background(), []*pb.RowKey{factorRowsKey("row-dup")}, []string{"close"}, nil)
	if err != nil || len(got) != 1 || got[0].GetFields()[0].GetValue().GetDoubleValue() != 2 {
		t.Fatalf("rows=%v err=%v", got, err)
	}
}

// 行的数据集必须与请求一致：一次写入只产生一个数据集的一条事件。
func TestUpsertFieldsRejectsRowsOfAnotherDataset(t *testing.T) {
	store := openFactorRowsStore(t)
	row := &pb.RowFieldUpsert{Key: factorRowsKey("row-a"), Fields: []*pb.FieldValue{doubleField("close", 1)}}
	if err := store.UpsertFields(context.Background(), "space", "another", []*pb.RowFieldUpsert{row}); err == nil {
		t.Fatal("row of another dataset: err = nil")
	}
	if head, err := store.ChangeCursor(); err != nil || head.GetSequence() != 0 {
		t.Fatalf("rejected write left changes: head=%v err=%v", head, err)
	}
}

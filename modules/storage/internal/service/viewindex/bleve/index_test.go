package bleve

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestBleveStructuredAndFullTextQuery(t *testing.T) {
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "bleve")})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "records", ViewVersion: 1, Engine: "bleve", SchemaHash: "hash", Columns: []*pb.ViewColumn{
		{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "score", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}}
	if err := index.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	row := func(id, title string, score float64) viewindex.RowWrite {
		return viewindex.RowWrite{Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "s", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: id, Version: "1"}}}}, Fields: []*pb.FieldValue{
			{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: title}}},
			{FieldId: "score", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: score}}},
		}}
	}
	if err := index.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{RowWrites: []viewindex.RowWrite{row("r1", "quant research", 0.9), row("r2", "macro note", 0.2)}, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := index.Query(context.Background(), "idx", viewindex.QuerySpec{TextQuery: "research", Groups: []viewindex.FilterGroup{{Conds: []viewindex.Filter{{Column: "score", Op: pb.FilterOp_FILTER_OP_GT, Values: []*pb.TypedValue{{Value: &pb.TypedValue_DoubleValue{DoubleValue: 0.5}}}}}}}, Limit: 10, TotalMode: pb.TotalMode_FORCE_EXACT})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].GetKey().GetRecord().GetRecordId() != "r1" {
		t.Fatalf("rows=%v total=%d err=%v", rows, total, err)
	}
	// LiveWrite is a field patch; omitted columns remain unchanged.
	if err := index.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{{
			Key:    viewindex.RowKey{Key: rows[0].GetKey()},
			Fields: []*pb.FieldValue{{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "updated"}}}},
		}},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	rows, _, err = index.Query(context.Background(), "idx", viewindex.QuerySpec{Keys: []*pb.RowKey{rows[0].GetKey()}, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("overwrite query failed: rows=%v err=%v", rows, err)
	}
	var title string
	var scorePresent bool
	for _, field := range rows[0].GetFields() {
		if field == nil {
			continue
		}
		switch field.GetFieldId() {
		case "title":
			title = field.GetValue().GetStringValue()
		case "score":
			scorePresent = field.GetValue() != nil
		}
	}
	if title != "updated" || !scorePresent {
		t.Fatalf("expected partial update to preserve score, got title=%q scorePresent=%v fields=%v", title, scorePresent, rows[0].GetFields())
	}
	if oldRows, _, err := index.Query(context.Background(), "idx", viewindex.QuerySpec{TextQuery: "quant", Limit: 10}); err != nil {
		t.Fatal(err)
	} else if len(oldRows) != 0 {
		t.Fatalf("old full-text value still matches after replacement: %v", oldRows)
	}
	if newRows, _, err := index.Query(context.Background(), "idx", viewindex.QuerySpec{TextQuery: "updated", Limit: 10}); err != nil {
		t.Fatal(err)
	} else if len(newRows) != 1 {
		t.Fatalf("new full-text value did not match: %v", newRows)
	}

	// Multiple partial writes for one RowKey in a single batch must merge
	// against the pending document before Bleve commits the batch.
	if err := index.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{
			{Key: viewindex.RowKey{Key: rows[0].GetKey()}, Fields: []*pb.FieldValue{{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "batch-title"}}}}},
			{Key: viewindex.RowKey{Key: rows[0].GetKey()}, Fields: []*pb.FieldValue{{FieldId: "score", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3.0}}}}},
		},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	rows, _, err = index.Query(context.Background(), "idx", viewindex.QuerySpec{Keys: []*pb.RowKey{rows[0].GetKey()}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("batch merge query failed: rows=%v err=%v", rows, err)
	}
	if !hasStringField(rows[0], "title", "batch-title") || !hasFloatField(rows[0], "score", 3.0) {
		t.Fatalf("same-batch partial writes lost fields: %v", rows[0])
	}
}

func TestConcurrentColdOpenDoesNotLeaveIndexLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bleve")
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "records", ViewVersion: 1, Engine: "bleve", SchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}}}
	first, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	index, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := index.get("idx")
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
}

func hasStringField(row *pb.RowFieldValues, name, want string) bool {
	for _, field := range row.GetFields() {
		if field != nil && field.GetFieldId() == name && field.GetValue().GetStringValue() == want {
			return true
		}
	}
	return false
}

func hasFloatField(row *pb.RowFieldValues, name string, want float64) bool {
	for _, field := range row.GetFields() {
		if field != nil && field.GetFieldId() == name && field.GetValue().GetDoubleValue() == want {
			return true
		}
	}
	return false
}

func TestBleveBackfillDoesNotOverwriteLiveRecordFields(t *testing.T) {
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "bleve")})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "records", ViewVersion: 1, Engine: "bleve", SchemaHash: "hash", Columns: []*pb.ViewColumn{
		{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "score", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}}
	if err := index.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r1", Version: "1"}}}
	field := func(id string, value *pb.TypedValue) *pb.FieldValue { return &pb.FieldValue{FieldId: id, Value: value} }
	if err := index.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{RowWrites: []viewindex.RowWrite{{Key: viewindex.RowKey{Key: key}, Fields: []*pb.FieldValue{
		field("title", &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "live"}}),
		field("score", &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}),
	}}}, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}); err != nil {
		t.Fatal(err)
	}
	if err := index.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{RowWrites: []viewindex.RowWrite{{Key: viewindex.RowKey{Key: key}, Fields: []*pb.FieldValue{
		field("title", &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "old"}}),
		field("score", &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}),
	}}}, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.Backfill}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := index.Query(context.Background(), "idx", viewindex.QuerySpec{Keys: []*pb.RowKey{key}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	values := map[string]string{}
	var score float64
	for _, f := range rows[0].GetFields() {
		values[f.GetFieldId()] = f.GetValue().GetStringValue()
		if f.GetFieldId() == "score" {
			score = f.GetValue().GetDoubleValue()
		}
	}
	if values["title"] != "live" || score != 2 {
		t.Fatalf("backfill overwrote live values: fields=%v", rows[0].GetFields())
	}
}

func TestBleveSubstringLikeAndMapping(t *testing.T) {
	if got := bleveSubstringPattern("research"); got != "*research*" {
		t.Fatalf("substring pattern=%q", got)
	}
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "bleve")})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "records", ViewVersion: 1, Engine: "bleve", SchemaHash: "hash", Columns: []*pb.ViewColumn{
		{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "score", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}}
	if err := index.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	if err := index.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{{
			Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "s", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r1", Version: "1"}}}},
			Fields: []*pb.FieldValue{
				{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "quant research note"}}},
				{FieldId: "score", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 0.8}}},
			},
		}},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := index.Query(context.Background(), "idx", viewindex.QuerySpec{
		Groups: []viewindex.FilterGroup{{Conds: []viewindex.Filter{{Column: "title", Op: pb.FilterOp_FILTER_OP_LIKE, Values: []*pb.TypedValue{{Value: &pb.TypedValue_StringValue{StringValue: "research"}}}}}}},
		Limit:  10, TotalMode: pb.TotalMode_FORCE_EXACT,
	})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("like rows=%v total=%d err=%v", rows, total, err)
	}
}

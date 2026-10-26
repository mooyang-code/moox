//go:build cgo

package duckdb

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 按列批量写入必须与改造前的实现（legacy_write_oracle_test.go）写出完全相同的 view_rows 与 view_series_counts。
// 每个场景把同一串批次分别写进两个索引，每批之后逐行逐列比较。

var equivalenceColumns = []struct {
	name      string
	valueType pb.FieldValueType
}{
	{"dbl", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	{"num", pb.FieldValueType_FIELD_VALUE_TYPE_INT},
	{"txt", pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
	{"flag", pb.FieldValueType_FIELD_VALUE_TYPE_BOOL},
	{"at", pb.FieldValueType_FIELD_VALUE_TYPE_TIME},
	{"doc", pb.FieldValueType_FIELD_VALUE_TYPE_JSON},
	{"raw", pb.FieldValueType_FIELD_VALUE_TYPE_BYTES},
}

func equivalenceSchema() viewindex.ViewIndexSchema {
	var columns []*pb.ViewColumn
	for i, column := range equivalenceColumns {
		columns = append(columns, &pb.ViewColumn{ColumnName: column.name, OriginId: column.name, SortOrder: uint32(i),
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, ValueType: column.valueType})
	}
	schema := viewindex.ViewIndexSchema{SpaceID: "space", ViewID: "equiv", PrimaryDatasetID: "dataset_x", ViewVersion: 1, Engine: "duckdb", Columns: columns}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	return schema
}

func equivalenceField(name string, seed int) *pb.FieldValue {
	var value *pb.TypedValue
	switch name {
	case "dbl":
		value = &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: float64(seed) + 0.25}}
	case "num":
		value = &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: int64(seed)}}
	case "txt":
		value = &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: fmt.Sprintf("t%d", seed)}}
	case "flag":
		value = &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: seed%2 == 0}}
	case "at":
		value = &pb.TypedValue{Value: &pb.TypedValue_TimeValue{TimeValue: time.Date(2026, 1, 1, 0, 0, seed, 0, time.UTC).Format(time.RFC3339Nano)}}
	case "doc":
		value = &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: fmt.Sprintf(`{"n":%d}`, seed)}}
	case "raw":
		value = &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: []byte{byte(seed), 1, 2}}}
	}
	return &pb.FieldValue{FieldId: name, Value: value}
}

func equivalenceNull(name string) *pb.FieldValue {
	return &pb.FieldValue{FieldId: name, Value: &pb.TypedValue{Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE_NULL}}}
}

func equivalenceAttr(key string, seed int) map[string]*pb.TypedValue {
	return map[string]*pb.TypedValue{key: {Value: &pb.TypedValue_StringValue{StringValue: fmt.Sprintf("h%d", seed)}}}
}

// equivalenceRow 生成一行：fields 为空则带全部列；nulls 里的列写显式 NULL；at 是 data_time 的文本。
func equivalenceRow(subject, at, tag string, seed int, fields []string, nulls []string, attrs map[string]*pb.TypedValue) viewindex.RowWrite {
	if fields == nil {
		for _, column := range equivalenceColumns {
			fields = append(fields, column.name)
		}
	}
	nullSet := map[string]bool{}
	for _, name := range nulls {
		nullSet[name] = true
	}
	var values []*pb.FieldValue
	for _, name := range fields {
		if nullSet[name] {
			values = append(values, equivalenceNull(name))
		} else {
			values = append(values, equivalenceField(name, seed))
		}
	}
	return viewindex.RowWrite{Key: viewindex.RowKey{Key: duckRowKey("space", "dataset_x", subject, "5m", at, tag)}, Fields: values, Attributes: attrs}
}

func dumpIndex(t *testing.T, manager *IndexManager, id string) string {
	t.Helper()
	db, _, _, _, err := manager.getIndex(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, query := range []string{
		`SELECT * FROM view_rows ORDER BY subject_id, freq, data_time, series_tag`,
		`SELECT * FROM view_series_counts ORDER BY subject_id, freq, series_tag`,
	} {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			cells := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range cells {
				pointers[i] = &cells[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			// view_rows 的列顺序由建表时遍历 map 决定，两个索引的顺序可能不同：按列名排序后再比。
			parts := make([]string, len(columns))
			for i, name := range columns {
				parts[i] = fmt.Sprintf("%s=%v", name, cells[i])
			}
			sort.Strings(parts)
			out.WriteString(strings.Join(parts, "|") + "\n")
		}
		_ = rows.Close()
		out.WriteString("----\n")
	}
	return out.String()
}

func TestBulkWriteMatchesLegacyWrite(t *testing.T) {
	ctx := context.Background()
	schema := equivalenceSchema()
	newManager := func(id string) *IndexManager {
		manager, err := OpenIndexManager(IndexManagerOptions{Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		if err := manager.Prepare(ctx, id, schema); err != nil {
			t.Fatal(err)
		}
		return manager
	}
	bulk, legacy := newManager("bulk"), newManager("legacy")

	apply := func(t *testing.T, mode viewindex.WriteMode, writes []viewindex.RowWrite) {
		t.Helper()
		if err := bulk.Write(ctx, "bulk", viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: 1, ViewSchemaHash: schema.SchemaHash, WriteMode: mode}); err != nil {
			t.Fatalf("按列批量写入: %v", err)
		}
		db, columns, _, _, err := legacy.getIndex(ctx, "legacy")
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := legacyWrite(ctx, tx, columns, writes, mode); err != nil {
			_ = tx.Rollback()
			t.Fatalf("改造前的写入: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if got, want := dumpIndex(t, bulk, "bulk"), dumpIndex(t, legacy, "legacy"); got != want {
			t.Fatalf("两种写法的结果不一致\n--- 按列批量\n%s\n--- 改造前\n%s", got, want)
		}
	}

	t0 := "2026-10-01T00:00:00Z"
	t1 := "2026-10-01T00:05:00Z"
	t2 := "2026-10-01T00:10:00Z"

	t.Run("新键整行含显式NULL与属性", func(t *testing.T) {
		apply(t, viewindex.LiveWrite, []viewindex.RowWrite{
			equivalenceRow("A-USDT", t0, "tag", 1, nil, nil, equivalenceAttr("factor.source_hash", 1)),
			equivalenceRow("A-USDT", t1, "tag", 2, nil, []string{"dbl", "txt", "doc", "raw"}, nil),
			equivalenceRow("B-USDT", t0, "tag", 3, nil, []string{"num", "flag", "at"}, equivalenceAttr("factor.source_hash", 3)),
			equivalenceRow("B-USDT", t0, "", 4, nil, nil, nil),
		})
	})
	t.Run("已存在键只更新一个字段其余保持", func(t *testing.T) {
		apply(t, viewindex.LiveWrite, []viewindex.RowWrite{
			equivalenceRow("A-USDT", t0, "tag", 10, []string{"dbl"}, nil, nil),
			equivalenceRow("B-USDT", t0, "tag", 11, []string{"txt", "num"}, nil, equivalenceAttr("factor.other", 11)),
		})
	})
	t.Run("显式NULL覆盖已有值", func(t *testing.T) {
		apply(t, viewindex.LiveWrite, []viewindex.RowWrite{
			equivalenceRow("A-USDT", t0, "tag", 12, []string{"dbl", "flag"}, []string{"dbl", "flag"}, nil),
		})
	})
	t.Run("新旧键混合且字段集合不同", func(t *testing.T) {
		apply(t, viewindex.LiveWrite, []viewindex.RowWrite{
			equivalenceRow("A-USDT", t2, "tag", 20, []string{"dbl", "num"}, nil, nil),
			equivalenceRow("A-USDT", t1, "tag", 21, []string{"dbl"}, nil, equivalenceAttr("factor.source_hash", 21)),
			equivalenceRow("C-USDT", t0, "tag", 22, []string{"txt"}, nil, nil),
			equivalenceRow("C-USDT", t1, "tag", 23, nil, nil, nil),
		})
	})
	t.Run("同一键重复出现与时间写法不同", func(t *testing.T) {
		apply(t, viewindex.LiveWrite, []viewindex.RowWrite{
			equivalenceRow("D-USDT", "2026-10-01T01:00:00Z", "tag", 30, []string{"dbl"}, nil, nil),
			equivalenceRow("D-USDT", "2026-10-01T01:00:00.000000000Z", "tag", 31, []string{"num"}, nil, equivalenceAttr("factor.source_hash", 31)),
			equivalenceRow("D-USDT", "2026-10-01T09:00:00+08:00", "tag", 32, []string{"txt"}, nil, equivalenceAttr("factor.source_hash", 32)),
		})
	})
	t.Run("回填只补空值并让既有属性优先", func(t *testing.T) {
		apply(t, viewindex.Backfill, []viewindex.RowWrite{
			equivalenceRow("A-USDT", t0, "tag", 40, nil, nil, equivalenceAttr("factor.source_hash", 40)),
			equivalenceRow("A-USDT", t1, "tag", 41, nil, nil, equivalenceAttr("factor.new", 41)),
			equivalenceRow("E-USDT", t0, "tag", 42, nil, []string{"dbl", "doc"}, equivalenceAttr("factor.source_hash", 42)),
			equivalenceRow("E-USDT", t1, "tag", 43, []string{"num"}, nil, nil),
		})
	})
	t.Run("回填遇到畸形JSON丢弃该字段", func(t *testing.T) {
		bad := equivalenceRow("F-USDT", t0, "tag", 50, []string{"dbl"}, nil, nil)
		bad.Fields = append(bad.Fields, &pb.FieldValue{FieldId: "doc", Value: &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: "{not json"}}})
		apply(t, viewindex.Backfill, []viewindex.RowWrite{bad})
	})
	t.Run("跨多个批次的大批量", func(t *testing.T) {
		var writes []viewindex.RowWrite
		base := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		for s := 0; s < 3; s++ {
			for i := 0; i < maxBulkWriteRows/2+7; i++ {
				writes = append(writes, equivalenceRow(fmt.Sprintf("G%d-USDT", s), base.Add(time.Duration(i)*5*time.Minute).Format(time.RFC3339), "tag", i, []string{"dbl", "num"}, nil, nil))
			}
		}
		apply(t, viewindex.LiveWrite, writes)
	})
}

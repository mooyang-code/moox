//go:build cgo

package duckdb

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// TestImportWriteThroughput 复现历史导入时 K 线 View 的写入：7 个 K 线列，每批 4000 行（4 个事件 × 1000 行，
// 同一标的连续时间），索引里已有 subjects × bars 行。对比现状 Write 与「按列整体绑定、一条语句写完一批」的原型。
// 默认跳过；MOOX_DUCK_BENCH=1 时运行。MOOX_DUCK_BENCH_SUBJECTS / MOOX_DUCK_BENCH_BARS 指定预置规模，
// MOOX_DUCK_BENCH_BATCHES 指定每种写法写多少批。
func TestImportWriteThroughput(t *testing.T) {
	if os.Getenv("MOOX_DUCK_BENCH") != "1" {
		t.Skip("设置 MOOX_DUCK_BENCH=1 运行写入基准")
	}
	envInt := func(name string, fallback int) int {
		if raw := os.Getenv(name); raw != "" {
			if value, err := strconv.Atoi(raw); err == nil {
				return value
			}
		}
		return fallback
	}
	subjects := envInt("MOOX_DUCK_BENCH_SUBJECTS", 500)
	bars := envInt("MOOX_DUCK_BENCH_BARS", 6000)
	batches := envInt("MOOX_DUCK_BENCH_BATCHES", 20)
	// MOOX_DUCK_BENCH_SHAPE=live 改为实时形态：每批是同一根 K 线的全部标的（每标的一行）。
	live := os.Getenv("MOOX_DUCK_BENCH_SHAPE") == "live"
	batchRows := 4000
	if live {
		batchRows = subjects
	}
	ctx := context.Background()

	doubles := []string{"close", "high", "low", "open", "quote_volume", "volume"}
	var columns []*pb.ViewColumn
	for i, name := range doubles {
		columns = append(columns, &pb.ViewColumn{ColumnName: name, OriginId: name, SortOrder: uint32(i),
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	}
	columns = append(columns, &pb.ViewColumn{ColumnName: "trade_num", OriginId: "trade_num", SortOrder: 99,
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT})
	schema := viewindex.ViewIndexSchema{SpaceID: "space", ViewID: "bench", PrimaryDatasetID: "dataset_kline_5m",
		ViewVersion: 1, Engine: "duckdb", Columns: columns}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	step := 5 * time.Minute
	subjectName := func(s int) string { return fmt.Sprintf("SYM%04d-USDT", s) }

	// open 建一个预置了 subjects × bars 行（与计数表）的索引。
	open := func(t *testing.T, id string) *IndexManager {
		manager, err := OpenIndexManager(IndexManagerOptions{Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		if err := manager.Prepare(ctx, id, schema); err != nil {
			t.Fatal(err)
		}
		db, _, _, _, err := manager.getIndex(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		for from := 0; from < bars; from += 500 {
			to := min(from+500, bars)
			statement := fmt.Sprintf(`INSERT INTO view_rows (subject_id, freq, data_time, series_tag, %s, trade_num)
				SELECT 'SYM' || lpad(CAST(s AS VARCHAR), 4, '0') || '-USDT', '5m',
				       CAST(TIMESTAMP '2026-01-01 00:00:00' + to_minutes(CAST(b * 5 AS BIGINT)) AS TIMESTAMP_NS), 'tag',
				       %s, CAST(b AS BIGINT)
				FROM range(%d, %d) AS bt(b), range(0, %d) AS st(s)`,
				joinQuoted(doubles), strings.TrimRight(strings.Repeat("random(),", len(doubles)), ","), from, to, subjects)
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("预置: %v", err)
			}
		}
		if _, err := db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO view_series_counts SELECT 'SYM' || lpad(CAST(s AS VARCHAR), 4, '0') || '-USDT', '5m', 'tag', %d FROM range(0, %d) AS st(s)`, bars, subjects)); err != nil {
			t.Fatalf("预置计数: %v", err)
		}
		t.Logf("预置 %d 标的 × %d 根 = %d 行，用时 %s", subjects, bars, subjects*bars, time.Since(began).Round(time.Millisecond))
		return manager
	}

	// batchWrites 生成第 n 批：一个标的在预置区间之后连续的 4000 根（全是新键，与导入的情形一致）。
	batchWrites := func(n int) []viewindex.RowWrite {
		subject := subjectName(n % subjects)
		first := bars + (n/subjects)*batchRows
		writes := make([]viewindex.RowWrite, 0, batchRows)
		for i := 0; i < batchRows; i++ {
			at := start.Add(time.Duration(first+i) * step).Format(time.RFC3339Nano)
			if live {
				subject = subjectName(i)
				at = start.Add(time.Duration(bars+n) * step).Format(time.RFC3339Nano)
			}
			fields := make([]*pb.FieldValue, 0, len(doubles)+1)
			for j, name := range doubles {
				fields = append(fields, &pb.FieldValue{FieldId: name, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: float64(i) + float64(j)/10}}})
			}
			fields = append(fields, &pb.FieldValue{FieldId: "trade_num", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: int64(i)}}})
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: duckRowKey("space", schema.PrimaryDatasetID, subject, "5m", at, "tag")}, Fields: fields})
		}
		return writes
	}

	run := func(t *testing.T, name string, write func(manager *IndexManager, id string, writes []viewindex.RowWrite) error) {
		id := "bench-" + name
		manager := open(t, id)
		var total time.Duration
		for n := 0; n < batches; n++ {
			writes := batchWrites(n)
			began := time.Now()
			if err := write(manager, id, writes); err != nil {
				t.Fatalf("%s 第 %d 批: %v", name, n, err)
			}
			total += time.Since(began)
		}
		rows := batches * batchRows
		db, _, _, _, _ := manager.getIndex(ctx, id)
		var count, counted int64
		_ = db.QueryRowContext(ctx, `SELECT count(*) FROM view_rows`).Scan(&count)
		_ = db.QueryRowContext(ctx, `SELECT sum(row_count) FROM view_series_counts`).Scan(&counted)
		t.Logf("%-10s %d 批 × %d 行：%s，%.0f 行/秒（表内 %d 行，计数 %d）", name, batches, batchRows,
			total.Round(time.Millisecond), float64(rows)/total.Seconds(), count, counted)
	}

	t.Run("现状", func(t *testing.T) {
		run(t, "current", func(manager *IndexManager, id string, writes []viewindex.RowWrite) error {
			return manager.Write(ctx, id, viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: 1, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite})
		})
	})
	t.Run("按列绑定", func(t *testing.T) {
		run(t, "lists", func(manager *IndexManager, id string, writes []viewindex.RowWrite) error {
			return writeByColumnLists(ctx, manager, id, schema, writes)
		})
	})
}

// writeByColumnLists 是原型：每个列绑定成一个 LIST 参数，用 unnest 展开，一批只执行两条语句
// （先按新键累加计数，再整批 UPSERT），代替现状的每 32 行一条、每行两轮。
func writeByColumnLists(ctx context.Context, manager *IndexManager, id string, schema viewindex.ViewIndexSchema, writes []viewindex.RowWrite) error {
	writes = collapseRowWrites(writes)
	db, columns, _, _, err := manager.getIndex(ctx, id)
	if err != nil {
		return err
	}
	names := []string{"subject_id", "freq", "data_time", "series_tag"}
	for _, column := range schema.Columns {
		names = append(names, column.GetColumnName())
	}
	lists := make([][]any, len(names))
	for i := range lists {
		lists[i] = make([]any, 0, len(writes))
	}
	for _, write := range writes {
		values, err := rowArgs(columns, names, write, viewindex.LiveWrite)
		if err != nil {
			return err
		}
		for i, value := range values {
			lists[i] = append(lists[i], value)
		}
	}
	typed := func(i int) string {
		switch {
		case names[i] == "data_time":
			return "TIMESTAMP_NS[]"
		case i < 4:
			return "VARCHAR[]"
		case columns[names[i]] == pb.FieldValueType_FIELD_VALUE_TYPE_INT:
			return "BIGINT[]"
		default:
			return "DOUBLE[]"
		}
	}
	selects := make([]string, len(names))
	args := make([]any, len(names))
	for i, name := range names {
		selects[i] = fmt.Sprintf("unnest(?::%s) AS %s", typed(i), quote(name))
		args[i] = toTypedSlice(lists[i], typed(i))
	}
	source := "SELECT " + strings.Join(selects, ", ")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	counts := `INSERT INTO view_series_counts (subject_id, freq, series_tag, row_count)
		SELECT s.subject_id, s.freq, s.series_tag, count(*) FROM (` + source + `) s
		WHERE NOT EXISTS (SELECT 1 FROM view_rows v WHERE v.subject_id = s.subject_id AND v.freq = s.freq AND v.data_time = s.data_time AND v.series_tag = s.series_tag)
		GROUP BY ALL
		ON CONFLICT (subject_id, freq, series_tag) DO UPDATE SET row_count = view_series_counts.row_count + excluded.row_count`
	if _, err := tx.ExecContext(ctx, counts, args...); err != nil {
		return fmt.Errorf("计数: %w", err)
	}
	sets := make([]string, 0, len(names)-4)
	for _, name := range names[4:] {
		sets = append(sets, fmt.Sprintf("%s = excluded.%s", quote(name), quote(name)))
	}
	upsert := fmt.Sprintf("INSERT INTO view_rows (%s) %s ON CONFLICT (subject_id, freq, data_time, series_tag) DO UPDATE SET %s",
		joinQuoted(names), source, strings.Join(sets, ", "))
	if _, err := tx.ExecContext(ctx, upsert, args...); err != nil {
		return fmt.Errorf("写入: %w", err)
	}
	return tx.Commit()
}

func toTypedSlice(values []any, sqlType string) any {
	switch sqlType {
	case "TIMESTAMP_NS[]":
		out := make([]time.Time, len(values))
		for i, value := range values {
			out[i], _ = value.(time.Time)
		}
		return out
	case "VARCHAR[]":
		out := make([]string, len(values))
		for i, value := range values {
			out[i], _ = value.(string)
		}
		return out
	case "BIGINT[]":
		out := make([]any, len(values))
		copy(out, values)
		return out
	default:
		out := make([]any, len(values))
		copy(out, values)
		return out
	}
}

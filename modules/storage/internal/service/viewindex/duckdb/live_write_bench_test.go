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

// TestLiveWriteBaseline 复现线上因子结果 View 的实时写入（每分钟约 500 个标的、13 个因子列），
// 对比不同写法的耗时。默认跳过；MOOX_DUCK_BENCH=1 时运行，MOOX_DUCK_BENCH_BARS 指定预置的历史根数（默认 1000）。
func TestLiveWriteBaseline(t *testing.T) {
	if os.Getenv("MOOX_DUCK_BENCH") != "1" {
		t.Skip("设置 MOOX_DUCK_BENCH=1 运行写入基准")
	}
	bars := 1000
	if raw := os.Getenv("MOOX_DUCK_BENCH_BARS"); raw != "" {
		bars, _ = strconv.Atoi(raw)
	}
	const subjects = 500
	ctx := context.Background()
	manager, err := OpenIndexManager(IndexManagerOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = manager.Close() }()

	// 18 个列：与线上 view_factor_*_1m 一致（5 个行情列 + trade_num + volume 为 K 线列，其余 13 个因子列中的 11 个 double）。
	klineCols := []string{"close", "high", "low", "open", "quote_volume", "volume"}
	factorCols := []string{"bias_20", "bias_q_20", "cci", "minmax_20", "quote_volume_mean_20", "quote_volume_mean_q_20",
		"volume_mean_q_20", "zf_abs_mean_20", "zf_mean_q_20", "zf_std_20", "zscore_abs_mean_q_20"}
	var columns []*pb.ViewColumn
	for i, name := range append(append([]string{}, klineCols...), factorCols...) {
		columns = append(columns, &pb.ViewColumn{ColumnName: name, OriginId: name, SortOrder: uint32(i),
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	}
	columns = append(columns, &pb.ViewColumn{ColumnName: "trade_num", OriginId: "trade_num", SortOrder: 99,
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT})
	schema := viewindex.ViewIndexSchema{SpaceID: "space", ViewID: "bench", PrimaryDatasetID: "dataset_factor_bench_1m",
		ViewVersion: 1, Engine: "duckdb", Columns: columns}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	if err := manager.Prepare(ctx, "bench-a", schema); err != nil {
		t.Fatal(err)
	}
	db, _, _, _, err := manager.getIndex(ctx, "bench-a")
	if err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	barTime := func(i int) time.Time { return start.Add(time.Duration(i) * time.Minute) }

	// 预置历史：用 SQL 批量灌入 bars × subjects 行，和线上索引的体量一致。
	prefillStart := time.Now()
	for from := 0; from < bars; from += 100 {
		to := min(from+100, bars)
		statement := fmt.Sprintf(`INSERT INTO view_rows (subject_id, freq, data_time, series_tag, %s)
			SELECT 'SYM' || lpad(CAST(s AS VARCHAR), 4, '0') || '-USDT', '1m',
			       CAST(TIMESTAMP '2026-10-01 00:00:00' + to_minutes(CAST(b AS BIGINT)) AS TIMESTAMP_NS), '',
			       %s
			FROM range(%d, %d) AS bt(b), range(0, %d) AS st(s)`,
			joinQuoted(append(append([]string{}, klineCols...), factorCols...)),
			strings.TrimRight(strings.Repeat("random(),", len(klineCols)+len(factorCols)), ","), from, to, subjects)
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("预置历史: %v", err)
		}
	}
	t.Logf("预置 %d 根 × %d 标的 = %d 行，用时 %s", bars, subjects, bars*subjects, time.Since(prefillStart).Round(time.Millisecond))

	factorWrites := func(bar int) []viewindex.RowWrite {
		writes := make([]viewindex.RowWrite, 0, subjects)
		for s := 0; s < subjects; s++ {
			fields := make([]*pb.FieldValue, 0, len(factorCols))
			for _, name := range factorCols {
				fields = append(fields, &pb.FieldValue{FieldId: name, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: float64(s) + 0.5}}})
			}
			writes = append(writes, viewindex.RowWrite{
				Key:        viewindex.RowKey{Key: duckRowKey("space", schema.PrimaryDatasetID, fmt.Sprintf("SYM%04d-USDT", s), "1m", barTime(bar).Format(time.RFC3339Nano), "")},
				Fields:     fields,
				Attributes: map[string]*pb.TypedValue{"factor.source_hash": {Value: &pb.TypedValue_StringValue{StringValue: "sha256:abcdef"}}},
			})
		}
		return writes
	}
	timeIt := func(name string, run func() error) {
		began := time.Now()
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%-46s %s", name, time.Since(began).Round(time.Millisecond))
	}
	write := func(bar int) error {
		return manager.Write(ctx, "bench-a", viewindex.ViewIndexWriteBatch{RowWrites: factorWrites(bar), ViewRevision: 1, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite})
	}

	// A：线上现状。新一根 bar 的 500 行（键不存在，走插入分支）。
	for i := 0; i < 3; i++ {
		bar := bars + i
		timeIt(fmt.Sprintf("A 现状 UPSERT·新键 第%d次", i+1), func() error { return write(bar) })
	}
	// B：键已存在（K 线事件先写了行，因子事件再补因子列，走冲突更新分支）。
	for i := 0; i < 3; i++ {
		bar := bars - 1 - i
		timeIt(fmt.Sprintf("B 现状 UPSERT·键已存在 第%d次", i+1), func() error { return write(bar) })
	}
	// 以下用原始 SQL 隔离各部分的开销。每个变体写一根全新的 bar（键不存在）。
	_, typeMap, _, _, err := manager.getIndex(ctx, "bench-a")
	if err != nil {
		t.Fatal(err)
	}
	namesWith := append(append([]string{"subject_id", "freq", "data_time", "series_tag"}, factorCols...), internalAttributesColumn)
	namesNoAttr := append(append([]string{"subject_id", "freq", "data_time", "series_tag"}, factorCols...))
	runRaw := func(name string, bar int, names []string, chunk int, build func(names []string, rows int) string) {
		timeIt(name, func() error {
			writes := factorWrites(bar)
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback() }()
			for from := 0; from < len(writes); from += chunk {
				to := min(from+chunk, len(writes))
				args := make([]any, 0, (to-from)*len(names))
				for _, write := range writes[from:to] {
					values, err := rowArgs(typeMap, names, write, viewindex.LiveWrite)
					if err != nil {
						return err
					}
					args = append(args, values...)
				}
				if _, err := tx.ExecContext(ctx, build(names, to-from), args...); err != nil {
					return err
				}
			}
			return tx.Rollback()
		})
	}
	upsert := func(names []string, rows int) string { return upsertSQLBatch(names, viewindex.LiveWrite, rows) }
	plain := func(names []string, rows int) string {
		return upsertSQLPlainInsert(names, rows)
	}
	runRaw("D1 现状 SQL（256 行/条，含 JSON 属性）", bars+20, namesWith, 256, upsert)
	runRaw("D2 同上但不带 JSON 属性列", bars+21, namesNoAttr, 256, upsert)
	runRaw("D3 纯 INSERT（无 ON CONFLICT），含 JSON", bars+22, namesWith, 256, plain)
	runRaw("D4 纯 INSERT，不带 JSON", bars+23, namesNoAttr, 256, plain)
	runRaw("D5 UPSERT 每条 32 行", bars+24, namesWith, 32, upsert)
	runRaw("D6 UPSERT 每条 500 行（一条语句）", bars+25, namesWith, 500, upsert)
	runRaw("D7 UPSERT 每条 1 行", bars+26, namesWith, 1, upsert)
	// 扫描每条语句的行数，找出拐点。
	for i, chunk := range []int{4, 8, 16, 24, 32, 48, 64, 100, 128} {
		runRaw(fmt.Sprintf("E 扫描 UPSERT 每条 %d 行", chunk), bars+30+i, namesWith, chunk, upsert)
	}
}

func upsertSQLPlainInsert(names []string, rowCount int) string {
	placeholders := "(" + strings.TrimRight(strings.Repeat("?,", len(names)), ",") + ")"
	values := make([]string, rowCount)
	for i := range values {
		values[i] = placeholders
	}
	return fmt.Sprintf("INSERT INTO view_rows (%s) VALUES %s", joinQuoted(names), strings.Join(values, ","))
}

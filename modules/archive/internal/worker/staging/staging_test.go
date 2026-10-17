package staging

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	day0 = time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	day1 = day0.AddDate(0, 0, 1)
	now0 = time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), ".state", "staging.duckdb"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func klineTask() Task {
	return Task{SpaceID: "crypto", DatasetID: "ds_1m", Columns: []Column{{"open", TypeDouble}, {"close", TypeDouble}, {"note", TypeString}, {"n", TypeInt}}}
}

func utcDay(at time.Time) PartitionRef {
	start := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	return PartitionRef{Key: start.Format("2006-01-02"), Start: start, End: start.AddDate(0, 0, 1)}
}

func row(subject string, at time.Time, tag string, values ...any) Row {
	return Row{SubjectID: subject, DataTime: at, SeriesTag: tag, Values: values}
}

func readGzip(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestStageWritesOnlyNewAndChangedRows(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	if err := s.EnsureTable(ctx, task); err != nil {
		t.Fatal(err)
	}
	at := day0.Add(10 * time.Hour)
	rows := []Row{
		row("BTC", at, "", 1.0, 2.0, "a", int64(1)),
		row("BTC", at.Add(time.Minute), "", 1.5, nil, nil, nil),
		row("ETH", at, "", 3.0, 4.0, "", int64(2)),
	}
	res, err := s.Stage(ctx, task, rows, utcDay, now0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 3 || res.Changed != 0 || len(res.Touched) != 1 || res.Touched["2026-10-15"].Start != day0 {
		t.Fatalf("first stage = %+v", res)
	}
	// 值没变：不产生写入，也不动 last_changed_at。
	later := now0.Add(time.Hour)
	res, err = s.Stage(ctx, task, rows, utcDay, later)
	if err != nil || res.Added != 0 || res.Changed != 0 || len(res.Touched) != 0 {
		t.Fatalf("unchanged stage = %+v err=%v", res, err)
	}
	p, ok, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if !ok || !p.LastChangedAt.Equal(now0) || p.Status != StatusOpen || !p.ExportedAt.IsZero() {
		t.Fatalf("partition = %+v", p)
	}
	// 一个值变了、一个 NULL 变成有值、一个新行：changed=2 added=1。
	rows[0].Values[1] = 2.5
	rows[1].Values[1] = 9.0
	res, err = s.Stage(ctx, task, append(rows, row("SOL", at, "", 7.0, 7.0, "z", int64(3))), utcDay, later)
	if err != nil || res.Added != 1 || res.Changed != 2 {
		t.Fatalf("changed stage = %+v err=%v", res, err)
	}
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if !p.LastChangedAt.Equal(later) {
		t.Fatalf("last_changed_at 应更新: %+v", p)
	}
	stats, err := s.Stats(ctx, task, p)
	if err != nil || stats.Rows != 4 || stats.Subjects != 3 || !stats.Min.Equal(at) || !stats.Max.Equal(at.Add(time.Minute)) {
		t.Fatalf("stats = %+v err=%v", stats, err)
	}
	// NULL 变成空串也是变化（二者在文件里可区分）。
	rows[1].Values[2] = ""
	if res, _ := s.Stage(ctx, task, rows[1:2], utcDay, later); res.Changed != 1 {
		t.Fatalf("NULL→空串应算变化: %+v", res)
	}
}

func TestStageDedupesKeysWithinABatchAndSplitsPartitions(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	at := day0.Add(23*time.Hour + 59*time.Minute)
	res, err := s.Stage(ctx, task, []Row{
		row("BTC", at, "", 1.0, 1.0, nil, nil),
		row("BTC", at, "", 2.0, 2.0, nil, nil), // 同键，以最后一条为准
		row("BTC", at.Add(time.Minute), "", 3.0, 3.0, nil, nil),
	}, utcDay, now0)
	if err != nil || res.Added != 2 || len(res.Touched) != 2 || res.Touched["2026-10-16"].Start != day1 {
		t.Fatalf("res = %+v err=%v", res, err)
	}
	var open float64
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT "open" FROM %s WHERE data_time = ?`, quote(task.Table())), at).Scan(&open); err != nil || open != 2.0 {
		t.Fatalf("open=%v err=%v", open, err)
	}
	bad := Row{SubjectID: "X", DataTime: at, Values: []any{1.0}}
	if _, err := s.Stage(ctx, task, []Row{bad}, utcDay, now0); err == nil {
		t.Fatal("值个数不对必须报错")
	}
}

func TestStageHandlesLargeBatches(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	var rows []Row
	for i := 0; i < 2500; i++ {
		rows = append(rows, row(fmt.Sprintf("S%03d", i%50), day0.Add(time.Duration(i/50)*time.Minute), "", float64(i), float64(i), nil, int64(i)))
	}
	res, err := s.Stage(ctx, task, rows, utcDay, now0)
	if err != nil || res.Added != 2500 {
		t.Fatalf("res = %+v err=%v", res, err)
	}
}

func TestEnsureTableAddsNewColumnsAndRejectsBadNames(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", day0, "", 1.0, 1.0, "a", int64(1))}, utcDay, now0)
	wider := task
	wider.Columns = append(append([]Column{}, task.Columns...), Column{"volume", TypeDouble}, Column{"ok", TypeBool}, Column{"at", TypeTime})
	if err := s.EnsureTable(ctx, wider); err != nil {
		t.Fatal(err)
	}
	var vol *float64
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT volume FROM %s`, quote(task.Table()))).Scan(&vol); err != nil || vol != nil {
		t.Fatalf("旧行的新列应为 NULL: %v err=%v", vol, err)
	}
	res, err := s.Stage(ctx, wider, []Row{row("BTC", day0, "", 1.0, 1.0, "a", int64(1), 5.0, true, day0)}, utcDay, now0)
	if err != nil || res.Changed != 1 {
		t.Fatalf("新列有值应算变化: %+v err=%v", res, err)
	}
	for _, name := range []string{"Open", "a-b", "subject_id", "1x", ""} {
		bad := Task{SpaceID: "crypto", DatasetID: "d", Columns: []Column{{name, TypeDouble}}}
		if err := s.EnsureTable(ctx, bad); err == nil {
			t.Fatalf("列名 %q 必须被拒绝", name)
		}
	}
	if err := s.EnsureTable(ctx, Task{SpaceID: "c", DatasetID: "d", Columns: []Column{{"x", "weird"}}}); err == nil {
		t.Fatal("未知类型必须被拒绝")
	}
	if err := s.EnsureTable(ctx, Task{SpaceID: "c", DatasetID: "d", Columns: []Column{{"x", TypeInt}, {"x", TypeInt}}}); err == nil {
		t.Fatal("重复列必须被拒绝")
	}
}

func TestExportWritesSortedGzipCSVThatKeepsEmptyStringsAndNulls(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	at := day0.Add(time.Hour)
	_, err := s.Stage(ctx, task, []Row{
		row("ETH", at, "", 3.0, 4.0, "", int64(2)),
		row("BTC", at.Add(time.Minute), "x,y", 1.0, nil, nil, nil),
		row("BTC", at, "", 65432.12, 2.0, `q"t`, int64(-1)),
		row("BTC", day1, "", 9.0, 9.0, nil, nil), // 属于下一个分区
	}, utcDay, now0)
	if err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	dest := filepath.Join(t.TempDir(), "out.csv.gz.partial")
	if err := s.Export(ctx, task, p, dest); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`data_time,subject_id,series_tag,open,close,note,n`,
		`2026-10-15T01:00:00Z,BTC,"",65432.12,2.0,"q""t",-1`,
		`2026-10-15T01:01:00Z,BTC,"x,y",1.0,,,`,
		`2026-10-15T01:00:00Z,ETH,"",3.0,4.0,"",2`,
	}, "\n") + "\n"
	if got := readGzip(t, dest); got != want {
		t.Fatalf("csv =\n%s\nwant\n%s", got, want)
	}
	header, err := CSVHeader(dest)
	if err != nil || strings.Join(header, ",") != "data_time,subject_id,series_tag,open,close,note,n" {
		t.Fatalf("header = %v err=%v", header, err)
	}
	if err := s.MarkExported(ctx, "crypto", "ds_1m", "2026-10-15", now0.Add(time.Hour), fileSHA(t, dest)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkExported(ctx, "crypto", "ds_1m", "nope", now0, ""); err == nil {
		t.Fatal("未登记的分区不能标记已导出")
	}
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if p.Status != StatusExported || p.NeedsExport() || p.SHA256 == "" || p.ExportCount != 1 {
		t.Fatalf("partition = %+v", p)
	}
	// 导出之后又有变化：需要重写。
	_, _ = s.Stage(ctx, task, []Row{row("BTC", at, "", 65432.12, 3.0, `q"t`, int64(-1))}, utcDay, now0.Add(2*time.Hour))
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if !p.NeedsExport() || p.Status != StatusExported {
		t.Fatalf("导出后有变化应需要重写: %+v", p)
	}
}

func TestFinalizeDeletesRowsAndAdvancesTheWatermarkAtomically(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	at := day0.Add(time.Hour)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", at, "", 1.0, 1.0, nil, nil), row("BTC", day1.Add(time.Hour), "", 2.0, 2.0, nil, nil)}, utcDay, now0)
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	w := now0.Add(48 * time.Hour)
	// 有未导出的变化：拒绝定稿，水位也不动。
	if err := s.Finalize(ctx, task, []string{"2026-10-15"}, w, now0, true); err == nil {
		t.Fatal("有未导出的变化不能定稿")
	}
	if _, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); ok {
		t.Fatal("定稿失败时水位不能推进")
	}
	dest := filepath.Join(t.TempDir(), "p.csv.gz")
	if err := s.Export(ctx, task, p, dest); err != nil {
		t.Fatal(err)
	}
	_ = s.MarkExported(ctx, "crypto", "ds_1m", "2026-10-15", now0.Add(time.Minute), "sha")
	// 同一事务里有一个不存在的分区：整体回滚，已存在分区仍是 exported、行还在、水位不动。
	if err := s.Finalize(ctx, task, []string{"2026-10-15", "2026-09-01"}, w, now0, true); err == nil {
		t.Fatal("包含不存在的分区应整体失败")
	}
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	stats, _ := s.Stats(ctx, task, p)
	if p.Status != StatusExported || stats.Rows != 1 {
		t.Fatalf("回滚后分区应不变: %+v %+v", p, stats)
	}
	if _, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); ok {
		t.Fatal("回滚后水位不能推进")
	}
	if err := s.Finalize(ctx, task, []string{"2026-10-15"}, w, now0, true); err != nil {
		t.Fatal(err)
	}
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	stats, _ = s.Stats(ctx, task, p)
	if p.Status != StatusFinal || stats.Rows != 0 || p.NeedsExport() {
		t.Fatalf("定稿后: %+v %+v", p, stats)
	}
	other, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-16")
	if otherStats, _ := s.Stats(ctx, task, other); otherStats.Rows != 1 {
		t.Fatalf("其他分区的行不能被删: %+v", otherStats)
	}
	if got, ok, err := s.Watermark(ctx, "crypto", "ds_1m"); err != nil || !ok || !got.Equal(w) {
		t.Fatalf("watermark = %v %v %v", got, ok, err)
	}
	// 只推进水位、不定稿任何分区（运行没有可定稿的分区时）。
	w2 := w.Add(time.Hour)
	if err := s.Finalize(ctx, task, nil, w2, now0, true); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Watermark(ctx, "crypto", "ds_1m"); !got.Equal(w2) {
		t.Fatalf("watermark = %v", got)
	}
}

func TestReopenFinalReadsTheFileBackSoRepeatedRowsDoNotRewriteIt(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	at := day0.Add(time.Hour)
	rows := []Row{
		row("BTC", at, "", 1.0, 2.0, "", int64(5)),
		row("BTC", at.Add(time.Minute), "x,y", 1.5, nil, nil, nil),
	}
	_, _ = s.Stage(ctx, task, rows, utcDay, now0)
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	file := filepath.Join(t.TempDir(), "p.csv.gz")
	_ = s.Export(ctx, task, p, file)
	exportedAt := now0.Add(time.Hour)
	_ = s.MarkExported(ctx, "crypto", "ds_1m", "2026-10-15", exportedAt, fileSHA(t, file))
	if err := s.Finalize(ctx, task, []string{"2026-10-15"}, now0, now0, true); err != nil {
		t.Fatal(err)
	}
	// 数据集又加了一列：读回时旧文件没有这一列，补 NULL。
	wider := task
	wider.Columns = append(append([]Column{}, task.Columns...), Column{"volume", TypeDouble})
	if err := s.EnsureTable(ctx, wider); err != nil {
		t.Fatal(err)
	}
	if err := s.ReopenFinal(ctx, wider, "2026-10-15", file); err != nil {
		t.Fatal(err)
	}
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if p.Status != StatusOpen || p.NeedsExport() {
		t.Fatalf("重新打开后不应需要重写: %+v", p)
	}
	stats, _ := s.Stats(ctx, wider, p)
	if stats.Rows != 2 || stats.Subjects != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	// 读回的行与原文件逐字段一致：再次写入同样的行（新列为 NULL）没有变化。
	same := []Row{
		row("BTC", at, "", 1.0, 2.0, "", int64(5), nil),
		row("BTC", at.Add(time.Minute), "x,y", 1.5, nil, nil, nil, nil),
	}
	res, err := s.Stage(ctx, wider, same, utcDay, now0.Add(2*time.Hour))
	if err != nil || res.Changed != 0 || res.Added != 0 {
		t.Fatalf("读回后重复的行不应有变化: %+v err=%v", res, err)
	}
	if p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15"); p.NeedsExport() {
		t.Fatalf("分区不应需要重写: %+v", p)
	}
	// 晚到的新行：需要重写，并且重写出的文件包含原有的全部行。
	res, _ = s.Stage(ctx, wider, []Row{row("ETH", at, "", 9.0, 9.0, nil, nil, nil)}, utcDay, now0.Add(3*time.Hour))
	if res.Added != 1 {
		t.Fatalf("res = %+v", res)
	}
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if !p.NeedsExport() {
		t.Fatalf("晚到的行应触发重写: %+v", p)
	}
	rewritten := filepath.Join(t.TempDir(), "again.csv.gz")
	if err := s.Export(ctx, wider, p, rewritten); err != nil {
		t.Fatal(err)
	}
	got := readGzip(t, rewritten)
	for _, want := range []string{`BTC,"",1.0,2.0,"",5,`, `BTC,"x,y",1.5,,,,`, `ETH,"",9.0,9.0,,,`, "data_time,subject_id,series_tag,open,close,note,n,volume"} {
		if !strings.Contains(got, want) {
			t.Fatalf("重写的文件缺少 %q:\n%s", want, got)
		}
	}
	// 只有定稿的分区才能读回。
	if err := s.ReopenFinal(ctx, wider, "2026-10-15", file); err == nil {
		t.Fatal("open 状态的分区不能重复读回")
	}
	if err := s.ReopenFinal(ctx, wider, "2026-10-15", filepath.Join(t.TempDir(), "missing.csv.gz")); err == nil {
		t.Fatal("文件缺失必须报错")
	}
}

func TestDropTaskRemovesTablesAndState(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", day0, "", 1.0, 1.0, nil, nil)}, utcDay, now0)
	_ = s.Finalize(ctx, task, nil, now0, now0, true)
	tables, _ := s.StagedTables(ctx)
	if len(tables) != 1 || tables[0] != task.Table() {
		t.Fatalf("tables = %v", tables)
	}
	if err := s.DropTask(ctx, "crypto", "ds_1m"); err != nil {
		t.Fatal(err)
	}
	tables, _ = s.StagedTables(ctx)
	parts, _ := s.Partitions(ctx, "crypto", "ds_1m")
	if _, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); len(tables) != 0 || len(parts) != 0 || ok {
		t.Fatalf("tables=%v parts=%v watermark=%v", tables, parts, ok)
	}
}

func TestOpenValidatesLimitsAndReopensExistingState(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(filepath.Join(dir, "a.duckdb"), Options{MemoryLimit: "lots"}); err == nil {
		t.Fatal("非法内存上限必须报错")
	}
	path := filepath.Join(dir, "b.duckdb")
	s, err := Open(path, Options{MemoryLimit: "512MB", Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	task := klineTask()
	_ = s.EnsureTable(context.Background(), task)
	_, _ = s.Stage(context.Background(), task, []Row{row("BTC", day0, "", 1.0, 1.0, nil, nil)}, utcDay, now0)
	if err := s.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.FileBytes() == 0 || s.Path() != path {
		t.Fatalf("size=%d path=%s", s.FileBytes(), s.Path())
	}
	_ = s.Close()
	again, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	parts, _ := again.Partitions(context.Background(), "crypto", "ds_1m")
	if len(parts) != 1 {
		t.Fatalf("重新打开后分区状态应保留: %v", parts)
	}
}

func TestCompactShrinksTheFileAfterLargeDeletesAndKeepsTheData(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	big := Task{SpaceID: "crypto", DatasetID: "big", Columns: task.Columns}
	_ = s.EnsureTable(ctx, big)
	var rows []Row
	for i := 0; i < 8000; i++ {
		rows = append(rows, row(fmt.Sprintf("S%04d", i%200), day0.Add(time.Duration(i/200)*time.Minute), fmt.Sprintf("tag-%d", i), float64(i), float64(i)*1.5, fmt.Sprintf("note-%d", i), int64(i)))
	}
	if _, err := s.Stage(ctx, big, rows, utcDay, now0); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	// 小任务的数据排在大任务之后；删除大任务后，空闲块在文件中间，文件不会自动变小。
	_ = s.EnsureTable(ctx, task)
	if _, err := s.Stage(ctx, task, []Row{row("BTC", day0, "", 1.0, 1.0, nil, nil)}, utcDay, now0); err != nil {
		t.Fatal(err)
	}
	if err := s.Finalize(ctx, task, nil, now0, now0, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.DropTask(ctx, "crypto", "big"); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	before := s.FileBytes()
	sizes, err := s.Sizes(ctx)
	if err != nil || sizes.FileBytes == 0 || sizes.UsedBytes >= sizes.FileBytes*3/4 {
		t.Fatalf("删除大任务后应有大量空闲块: %+v err=%v", sizes, err)
	}
	// 阈值很高：不重建。
	if done, err := s.Compact(ctx, 2, before*10); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	done, err := s.Compact(ctx, 1.5, 0)
	if err != nil || !done {
		t.Fatalf("compact done=%v err=%v", done, err)
	}
	if after := s.FileBytes(); after >= before*3/4 {
		t.Fatalf("文件应明显变小: before=%d after=%d", before, after)
	}
	// 重建之后状态表、水位、暂存表都还在，并且能继续使用。
	if w, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); !ok || !w.Equal(now0) {
		t.Fatalf("watermark = %v %v", w, ok)
	}
	if parts, _ := s.Partitions(ctx, "crypto", "ds_1m"); len(parts) != 1 {
		t.Fatalf("partitions = %+v", parts)
	}
	if res, err := s.Stage(ctx, task, []Row{row("ETH", day0, "", 2.0, 2.0, nil, nil)}, utcDay, now0.Add(time.Hour)); err != nil || res.Added != 1 {
		t.Fatalf("重建后仍可写入: %+v err=%v", res, err)
	}
	var count int
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM %s`, quote(task.Table()))).Scan(&count); err != nil || count != 2 {
		t.Fatalf("重建后数据应保留: count=%d err=%v", count, err)
	}
	if _, err := os.Stat(s.Path() + ".old"); !os.IsNotExist(err) {
		t.Fatal("旧文件应被清理")
	}
	if err := s.CleanTemp(); err != nil {
		t.Fatal(err)
	}
}

func TestPendingReportsTrackEveryChangeUntilTheManagerAcknowledges(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	at := day0.Add(time.Hour)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", at, "", 1.0, 1.0, nil, nil), row("ETH", at.Add(time.Minute), "", 2.0, 2.0, nil, nil)}, utcDay, now0)
	pending, err := s.PendingReports(ctx, "crypto", "ds_1m")
	if err != nil || len(pending) != 1 || pending[0].Key != "2026-10-15" || !pending[0].ReportPending {
		t.Fatalf("pending = %+v err=%v", pending, err)
	}
	if err := s.MarkReported(ctx, "crypto", "ds_1m", []string{"2026-10-15"}); err != nil {
		t.Fatal(err)
	}
	if pending, _ = s.PendingReports(ctx, "crypto", "ds_1m"); len(pending) != 0 {
		t.Fatalf("确认登记后不应再待上报: %+v", pending)
	}
	// 值没变：不重新标记。
	_, _ = s.Stage(ctx, task, []Row{row("BTC", at, "", 1.0, 1.0, nil, nil)}, utcDay, now0.Add(time.Hour))
	if pending, _ = s.PendingReports(ctx, "crypto", "ds_1m"); len(pending) != 0 {
		t.Fatalf("没有变化不应待上报: %+v", pending)
	}
	// 导出、定稿都会重新标记；定稿后统计存在分区状态里。
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	dest := filepath.Join(t.TempDir(), "p.csv.gz")
	_ = s.Export(ctx, task, p, dest)
	_ = s.MarkExported(ctx, "crypto", "ds_1m", "2026-10-15", now0.Add(2*time.Hour), "sha")
	if pending, _ = s.PendingReports(ctx, "crypto", "ds_1m"); len(pending) != 1 || pending[0].ExportCount != 1 {
		t.Fatalf("导出后应待上报: %+v", pending)
	}
	_ = s.MarkReported(ctx, "crypto", "ds_1m", []string{"2026-10-15"})
	finalizedAt := now0.Add(72 * time.Hour)
	if err := s.Finalize(ctx, task, []string{"2026-10-15"}, finalizedAt, finalizedAt, true); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.PendingReports(ctx, "crypto", "ds_1m")
	if len(pending) != 1 || pending[0].Status != StatusFinal || pending[0].RowCount != 2 || pending[0].SubjectCount != 2 ||
		!pending[0].MinDataTime.Equal(at) || !pending[0].MaxDataTime.Equal(at.Add(time.Minute)) || !pending[0].FinalizedAt.Equal(finalizedAt) {
		t.Fatalf("定稿后的登记统计要保存在分区状态里: %+v", pending)
	}
	_ = s.MarkReported(ctx, "crypto", "ds_1m", []string{"2026-10-15"})
	// 重新打开已定稿的分区：待上报，定稿时间清空。
	if err := s.ReopenFinal(ctx, task, "2026-10-15", dest); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.PendingReports(ctx, "crypto", "ds_1m")
	if len(pending) != 1 || pending[0].Status != StatusOpen || !pending[0].FinalizedAt.IsZero() {
		t.Fatalf("重新打开后: %+v", pending)
	}
	if err := s.MarkReported(ctx, "crypto", "ds_1m", nil); err != nil {
		t.Fatal(err)
	}
}

func TestFirstStartIsKeptUntilTheWatermarkAdvances(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	if _, ok, err := s.FirstStart(ctx, "crypto", "ds_1m"); ok || err != nil {
		t.Fatalf("初始没有起点: %v %v", ok, err)
	}
	first := now0.Add(-2 * time.Hour)
	if err := s.SetFirstStart(ctx, "crypto", "ds_1m", first); err != nil {
		t.Fatal(err)
	}
	// 更晚的起点不能覆盖；更早的可以。
	_ = s.SetFirstStart(ctx, "crypto", "ds_1m", first.Add(time.Hour))
	if got, ok, _ := s.FirstStart(ctx, "crypto", "ds_1m"); !ok || !got.Equal(first) {
		t.Fatalf("first start = %v %v", got, ok)
	}
	_ = s.SetFirstStart(ctx, "crypto", "ds_1m", first.Add(-time.Hour))
	if got, _, _ := s.FirstStart(ctx, "crypto", "ds_1m"); !got.Equal(first.Add(-time.Hour)) {
		t.Fatalf("first start = %v", got)
	}
	if _, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); ok {
		t.Fatal("只记起点不等于有水位")
	}
	_ = s.EnsureTable(ctx, task)
	if err := s.Finalize(ctx, task, nil, now0, now0, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.FirstStart(ctx, "crypto", "ds_1m"); ok {
		t.Fatal("水位前进后首次起点应清除")
	}
	if w, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); !ok || !w.Equal(now0) {
		t.Fatalf("watermark = %v %v", w, ok)
	}
}

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
	s, err := Open(filepath.Join(t.TempDir(), ".state", "staging.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func klineTask() Task {
	return Task{SpaceID: "crypto", DatasetID: "ds_1m", Purpose: "collect", DirName: "binance-spot-kline-1m", Columns: []Column{{"open", TypeDouble}, {"close", TypeDouble}, {"note", TypeString}, {"n", TypeInt}}}
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

// exportFile 把分区导出成文件（路径以 .gz 结尾时 gzip 压缩），返回文件的 sha256。
func exportFile(t *testing.T, s *Store, task Task, p PartitionState, path string) string {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var w io.Writer = f
	var zw *gzip.Writer
	if strings.HasSuffix(path, ".gz") {
		zw = gzip.NewWriter(f)
		w = zw
	}
	if err := s.ExportCSV(context.Background(), task, p, w); err != nil {
		t.Fatal(err)
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return fileSHA(t, path)
}

// finalizeAll 定稿分区后推进水位（运行结束时的两步）。
func finalizeAll(ctx context.Context, s *Store, task Task, keys []string, watermark, finalizedAt time.Time) error {
	if err := s.Finalize(ctx, task, keys, finalizedAt); err != nil {
		return err
	}
	return s.AdvanceWatermark(ctx, task.SpaceID, task.DatasetID, watermark)
}

func stagedTables(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'staged\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return out
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
	res, err := s.Stage(ctx, task, rows, utcDay, now0, StageReplace)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 3 || res.Changed != 0 {
		t.Fatalf("first stage = %+v", res)
	}
	// 值没变：不产生写入，也不动 last_changed_at。
	later := now0.Add(time.Hour)
	res, err = s.Stage(ctx, task, rows, utcDay, later, StageReplace)
	if err != nil || res.Added != 0 || res.Changed != 0 {
		t.Fatalf("unchanged stage = %+v err=%v", res, err)
	}
	p, ok, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if !ok || !p.LastChangedAt.Equal(now0) || p.Status != StatusOpen || !p.ExportedAt.IsZero() {
		t.Fatalf("partition = %+v", p)
	}
	// 一个值变了、一个 NULL 变成有值、一个新行：changed=2 added=1。
	rows[0].Values[1] = 2.5
	rows[1].Values[1] = 9.0
	res, err = s.Stage(ctx, task, append(rows, row("SOL", at, "", 7.0, 7.0, "z", int64(3))), utcDay, later, StageReplace)
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
	if res, _ := s.Stage(ctx, task, rows[1:2], utcDay, later, StageReplace); res.Changed != 1 {
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
	}, utcDay, now0, StageReplace)
	if err != nil || res.Added != 2 {
		t.Fatalf("res = %+v err=%v", res, err)
	}
	var open float64
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT "open" FROM %s WHERE data_time = ?`, quote(task.Table())), nanos(at)).Scan(&open); err != nil || open != 2.0 {
		t.Fatalf("open=%v err=%v", open, err)
	}
	bad := Row{SubjectID: "X", DataTime: at, Values: []any{1.0}}
	if _, err := s.Stage(ctx, task, []Row{bad}, utcDay, now0, StageReplace); err == nil {
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
	res, err := s.Stage(ctx, task, rows, utcDay, now0, StageReplace)
	if err != nil || res.Added != 2500 {
		t.Fatalf("res = %+v err=%v", res, err)
	}
}

func TestEnsureTableAddsNewColumnsAndRejectsBadNames(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", day0, "", 1.0, 1.0, "a", int64(1))}, utcDay, now0, StageReplace)
	wider := task
	wider.Columns = append(append([]Column{}, task.Columns...), Column{"volume", TypeDouble}, Column{"ok", TypeBool}, Column{"at", TypeTime})
	if err := s.EnsureTable(ctx, wider); err != nil {
		t.Fatal(err)
	}
	var vol *float64
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT volume FROM %s`, quote(task.Table()))).Scan(&vol); err != nil || vol != nil {
		t.Fatalf("旧行的新列应为 NULL: %v err=%v", vol, err)
	}
	res, err := s.Stage(ctx, wider, []Row{row("BTC", day0, "", 1.0, 1.0, "a", int64(1), 5.0, true, day0)}, utcDay, now0, StageReplace)
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
	}, utcDay, now0, StageReplace)
	if err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	dest := filepath.Join(t.TempDir(), "out.csv.gz")
	exportFile(t, s, task, p, dest)
	want := strings.Join([]string{
		`data_time,subject_id,series_tag,open,close,note,n`,
		`2026-10-15T01:00:00Z,BTC,"",65432.12,2.0,"q""t",-1`,
		`2026-10-15T01:01:00Z,BTC,"x,y",1.0,,,`,
		`2026-10-15T01:00:00Z,ETH,"",3.0,4.0,"",2`,
	}, "\n") + "\n"
	if got := readGzip(t, dest); got != want {
		t.Fatalf("csv =\n%s\nwant\n%s", got, want)
	}
	if err := s.MarkExported(ctx, "crypto", "ds_1m", "2026-10-15", now0.Add(time.Hour), "rel.csv", fileSHA(t, dest)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkExported(ctx, "crypto", "ds_1m", "nope", now0, "rel.csv", ""); err == nil {
		t.Fatal("未登记的分区不能标记已导出")
	}
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if p.Status != StatusExported || p.NeedsExport() || p.SHA256 == "" || p.ExportCount != 1 || p.FilePath != "rel.csv" {
		t.Fatalf("partition = %+v", p)
	}
	// 导出之后又有变化：需要重写。
	_, _ = s.Stage(ctx, task, []Row{row("BTC", at, "", 65432.12, 3.0, `q"t`, int64(-1))}, utcDay, now0.Add(2*time.Hour), StageReplace)
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if !p.NeedsExport() || p.Status != StatusExported {
		t.Fatalf("导出后有变化应需要重写: %+v", p)
	}
}

func TestFinalizeDeletesRowsPerPartitionAndTheWatermarkAdvancesSeparately(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	at := day0.Add(time.Hour)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", at, "", 1.0, 1.0, nil, nil), row("BTC", day1.Add(time.Hour), "", 2.0, 2.0, nil, nil)}, utcDay, now0, StageReplace)
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	w := now0.Add(48 * time.Hour)
	// 有未导出的变化：拒绝定稿。
	if err := s.Finalize(ctx, task, []string{"2026-10-15"}, now0); err == nil {
		t.Fatal("有未导出的变化不能定稿")
	}
	dest := filepath.Join(t.TempDir(), "p.csv.gz")
	exportFile(t, s, task, p, dest)
	_ = s.MarkExported(ctx, "crypto", "ds_1m", "2026-10-15", now0.Add(time.Minute), "rel.csv", "sha")
	// 每个分区一个事务：前一个分区定稿成功，后一个不存在的分区报错，已定稿的不回滚。
	if err := s.Finalize(ctx, task, []string{"2026-10-15", "2026-09-01"}, now0); err == nil {
		t.Fatal("不存在的分区应报错")
	}
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	stats, _ := s.Stats(ctx, task, p)
	if p.Status != StatusFinal || stats.Rows != 0 || p.NeedsExport() || p.RowCount != 1 {
		t.Fatalf("定稿后: %+v %+v", p, stats)
	}
	if _, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); ok {
		t.Fatal("定稿不推进水位")
	}
	// 已定稿的分区再定稿是空操作。
	if err := s.Finalize(ctx, task, []string{"2026-10-15"}, now0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15"); !again.FinalizedAt.Equal(now0) || again.RowCount != 1 {
		t.Fatalf("重复定稿不能改统计: %+v", again)
	}
	other, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-16")
	if otherStats, _ := s.Stats(ctx, task, other); otherStats.Rows != 1 {
		t.Fatalf("其他分区的行不能被删: %+v", otherStats)
	}
	if err := s.AdvanceWatermark(ctx, "crypto", "ds_1m", w); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.Watermark(ctx, "crypto", "ds_1m"); err != nil || !ok || !got.Equal(w) {
		t.Fatalf("watermark = %v %v %v", got, ok, err)
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
	_, _ = s.Stage(ctx, task, rows, utcDay, now0, StageReplace)
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	file := filepath.Join(t.TempDir(), "p.csv.gz")
	exportFile(t, s, task, p, file)
	exportedAt := now0.Add(time.Hour)
	_ = s.MarkExported(ctx, "crypto", "ds_1m", "2026-10-15", exportedAt, "rel.csv", fileSHA(t, file))
	if err := finalizeAll(ctx, s, task, []string{"2026-10-15"}, now0, now0); err != nil {
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
	if p.Status != StatusExported || p.NeedsExport() {
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
	res, err := s.Stage(ctx, wider, same, utcDay, now0.Add(2*time.Hour), StageReplace)
	if err != nil || res.Changed != 0 || res.Added != 0 {
		t.Fatalf("读回后重复的行不应有变化: %+v err=%v", res, err)
	}
	if p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15"); p.NeedsExport() {
		t.Fatalf("分区不应需要重写: %+v", p)
	}
	// 晚到的新行：需要重写，并且重写出的文件包含原有的全部行。
	res, _ = s.Stage(ctx, wider, []Row{row("ETH", at, "", 9.0, 9.0, nil, nil, nil)}, utcDay, now0.Add(3*time.Hour), StageReplace)
	if res.Added != 1 {
		t.Fatalf("res = %+v", res)
	}
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if !p.NeedsExport() {
		t.Fatalf("晚到的行应触发重写: %+v", p)
	}
	rewritten := filepath.Join(t.TempDir(), "again.csv.gz")
	exportFile(t, s, wider, p, rewritten)
	got := readGzip(t, rewritten)
	for _, want := range []string{`BTC,"",1.0,2.0,"",5,`, `BTC,"x,y",1.5,,,,`, `ETH,"",9.0,9.0,,,`, "data_time,subject_id,series_tag,open,close,note,n,volume"} {
		if !strings.Contains(got, want) {
			t.Fatalf("重写的文件缺少 %q:\n%s", want, got)
		}
	}
	// 只有定稿的分区才能读回。
	if err := s.ReopenFinal(ctx, wider, "2026-10-15", file); err == nil {
		t.Fatal("未定稿的分区不能重复读回")
	}
	if err := s.ReopenFinal(ctx, wider, "2026-10-15", filepath.Join(t.TempDir(), "missing.csv.gz")); err == nil {
		t.Fatal("文件缺失必须报错")
	}
}

func TestDropTaskRemovesTablesAndState(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", day0, "", 1.0, 1.0, nil, nil)}, utcDay, now0, StageReplace)
	_ = finalizeAll(ctx, s, task, nil, now0, now0)
	tables := stagedTables(t, s)
	if len(tables) != 1 || tables[0] != task.Table() {
		t.Fatalf("tables = %v", tables)
	}
	if err := s.DropTask(ctx, "crypto", "ds_1m"); err != nil {
		t.Fatal(err)
	}
	tables = stagedTables(t, s)
	parts, _ := s.Partitions(ctx, "crypto", "ds_1m")
	if _, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); len(tables) != 0 || len(parts) != 0 || ok {
		t.Fatalf("tables=%v parts=%v watermark=%v", tables, parts, ok)
	}
}

func TestOpenValidatesLimitsAndReopensExistingState(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(filepath.Join(dir, "a.db"), Options{CacheBytes: 1024}); err == nil {
		t.Fatal("过小的缓存上限必须报错")
	}
	path := filepath.Join(dir, "b.db")
	s, err := Open(path, Options{CacheBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	task := klineTask()
	_ = s.EnsureTable(context.Background(), task)
	_, _ = s.Stage(context.Background(), task, []Row{row("BTC", day0, "", 1.0, 1.0, nil, nil)}, utcDay, now0, StageReplace)
	if err := s.Maintain(context.Background()); err != nil {
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

func TestPendingReportsTrackEveryChangeUntilTheManagerAcknowledges(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	at := day0.Add(time.Hour)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", at, "", 1.0, 1.0, nil, nil), row("ETH", at.Add(time.Minute), "", 2.0, 2.0, nil, nil)}, utcDay, now0, StageReplace)
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
	_, _ = s.Stage(ctx, task, []Row{row("BTC", at, "", 1.0, 1.0, nil, nil)}, utcDay, now0.Add(time.Hour), StageReplace)
	if pending, _ = s.PendingReports(ctx, "crypto", "ds_1m"); len(pending) != 0 {
		t.Fatalf("没有变化不应待上报: %+v", pending)
	}
	// 导出、定稿都会重新标记；定稿后统计存在分区状态里。
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	dest := filepath.Join(t.TempDir(), "p.csv.gz")
	exportFile(t, s, task, p, dest)
	_ = s.MarkExported(ctx, "crypto", "ds_1m", "2026-10-15", now0.Add(2*time.Hour), "rel.csv", "sha")
	if pending, _ = s.PendingReports(ctx, "crypto", "ds_1m"); len(pending) != 1 || pending[0].ExportCount != 1 {
		t.Fatalf("导出后应待上报: %+v", pending)
	}
	_ = s.MarkReported(ctx, "crypto", "ds_1m", []string{"2026-10-15"})
	finalizedAt := now0.Add(72 * time.Hour)
	if err := finalizeAll(ctx, s, task, []string{"2026-10-15"}, finalizedAt, finalizedAt); err != nil {
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
	if len(pending) != 1 || pending[0].Status != StatusExported || !pending[0].FinalizedAt.IsZero() {
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
	if err := finalizeAll(ctx, s, task, nil, now0, now0); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.FirstStart(ctx, "crypto", "ds_1m"); ok {
		t.Fatal("水位前进后首次起点应清除")
	}
	if w, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); !ok || !w.Equal(now0) {
		t.Fatalf("watermark = %v %v", w, ok)
	}
}

func TestNewDatabaseUsesIncrementalVacuumAndMaintainReturnsFreePages(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	var mode int
	if err := s.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode != 2 {
		t.Fatalf("auto_vacuum = %d err=%v，应为 2（INCREMENTAL）", mode, err)
	}
	big := Task{SpaceID: "crypto", DatasetID: "big", Columns: klineTask().Columns}
	_ = s.EnsureTable(ctx, big)
	var rows []Row
	for i := 0; i < 8000; i++ {
		rows = append(rows, row(fmt.Sprintf("S%04d", i%200), day0.Add(time.Duration(i/200)*time.Minute), fmt.Sprintf("tag-%d", i), float64(i), float64(i), fmt.Sprintf("note-%d", i), int64(i)))
	}
	if _, err := s.Stage(ctx, big, rows, utcDay, now0, StageReplace); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	before := s.FileBytes()
	if err := s.DropTask(ctx, "crypto", "big"); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	var free int
	if err := s.db.QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil || free != 0 {
		t.Fatalf("维护后空闲页应归还: free=%d err=%v", free, err)
	}
	if after := s.FileBytes(); after >= before/2 {
		t.Fatalf("文件应明显变小: before=%d after=%d", before, after)
	}
	if info, err := os.Stat(s.Path() + "-wal"); err == nil && info.Size() != 0 {
		t.Fatalf("WAL 应被截断: %d", info.Size())
	}
}

func TestBindTaskDropsStateOfAnotherTaskID(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	if err := s.BindTask(ctx, task, ""); err == nil {
		t.Fatal("缺少 task_id 必须报错")
	}
	if err := s.BindTask(ctx, Task{SpaceID: "crypto", DatasetID: "ds_1m"}, "7"); err == nil {
		t.Fatal("缺少用途或目录名必须报错")
	}
	if _, _, ok, err := s.TaskDir(ctx, "crypto", "ds_1m"); ok || err != nil {
		t.Fatalf("还没绑定时没有目录: ok=%v err=%v", ok, err)
	}
	if err := s.BindTask(ctx, task, "7"); err != nil {
		t.Fatal(err)
	}
	if purpose, dir, ok, err := s.TaskDir(ctx, "crypto", "ds_1m"); err != nil || !ok || purpose != "collect" || dir != "binance-spot-kline-1m" {
		t.Fatalf("TaskDir = %q %q %v %v", purpose, dir, ok, err)
	}
	_ = s.EnsureTable(ctx, task)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", day0, "", 1.0, 1.0, nil, nil)}, utcDay, now0, StageReplace)
	_ = finalizeAll(ctx, s, task, nil, now0, now0)
	// 同一任务再次绑定：保留全部状态。
	if err := s.BindTask(ctx, task, "7"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); !ok {
		t.Fatal("同一任务不应丢水位")
	}
	// 任务重建：旧水位、暂存表与分区状态全部丢弃。
	rebuilt := task
	rebuilt.DirName = "binance-spot-kline-1m-v2"
	if err := s.BindTask(ctx, rebuilt, "8"); err != nil {
		t.Fatal(err)
	}
	parts, _ := s.Partitions(ctx, "crypto", "ds_1m")
	if _, ok, _ := s.Watermark(ctx, "crypto", "ds_1m"); ok || len(parts) != 0 || len(stagedTables(t, s)) != 0 {
		t.Fatalf("旧任务状态应被丢弃: watermark=%v parts=%v", ok, parts)
	}
	var stored string
	_ = s.db.QueryRow(`SELECT task_id FROM task_state WHERE space_id = 'crypto' AND dataset_id = 'ds_1m'`).Scan(&stored)
	if stored != "8" {
		t.Fatalf("task_id = %q", stored)
	}
	if _, dir, _, _ := s.TaskDir(ctx, "crypto", "ds_1m"); dir != "binance-spot-kline-1m-v2" {
		t.Fatalf("重建后的目录名 = %q", dir)
	}
}

func TestReconcileDropsDatasetsThatAreNotInTheTaskList(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	for _, ds := range []string{"keep", "gone", "recreated"} {
		task := Task{SpaceID: "crypto", DatasetID: ds, Purpose: "collect", DirName: ds, Columns: klineTask().Columns}
		_ = s.BindTask(ctx, task, "1")
		_ = s.EnsureTable(ctx, task)
		if _, err := s.Stage(ctx, task, []Row{row("BTC", day0, "", 1.0, 1.0, nil, nil)}, utcDay, now0, StageReplace); err != nil {
			t.Fatal(err)
		}
	}
	dropped, err := s.Reconcile(ctx, map[string]string{"crypto/keep": "1", "crypto/recreated": "2", "crypto/other": "3"})
	if err != nil || strings.Join(dropped, ",") != "crypto/gone,crypto/recreated" {
		t.Fatalf("dropped = %v err=%v", dropped, err)
	}
	if tables := stagedTables(t, s); len(tables) != 1 || tables[0] != (Task{SpaceID: "crypto", DatasetID: "keep"}).Table() {
		t.Fatalf("tables = %v", tables)
	}
	for ds, want := range map[string]int{"keep": 1, "gone": 0, "recreated": 0} {
		if parts, _ := s.Partitions(ctx, "crypto", ds); len(parts) != want {
			t.Fatalf("%s: partitions = %v", ds, parts)
		}
	}
	// 空列表（没有任何任务）清掉全部。
	if dropped, err := s.Reconcile(ctx, map[string]string{}); err != nil || len(dropped) != 1 {
		t.Fatalf("dropped = %v err=%v", dropped, err)
	}
}

func TestRestorePartitionsRecoversExportedAsFinalAndKeepsLocalState(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	// 本地已有 10-16 的状态：登记不能覆盖它。
	_, _ = s.Stage(ctx, task, []Row{row("BTC", day1, "", 1.0, 1.0, nil, nil)}, utcDay, now0, StageReplace)
	exportedAt := now0.Add(-time.Hour)
	regs := []PartitionState{
		{Key: "2026-10-14", Start: day0.AddDate(0, 0, -1), End: day0, Status: StatusFinal, FilePath: "crypto/ds_1m/2026/20261014.csv.gz", SHA256: "a",
			ExportCount: 2, RowCount: 10, SubjectCount: 2, ExportedAt: exportedAt, LastChangedAt: exportedAt, FinalizedAt: exportedAt, MinDataTime: day0.Add(-time.Hour), MaxDataTime: day0.Add(-time.Minute)},
		{Key: "2026-10-15", Start: day0, End: day1, Status: StatusExported, FilePath: "crypto/ds_1m/2026/20261015.csv.gz", SHA256: "b",
			ExportCount: 1, RowCount: 5, SubjectCount: 1, ExportedAt: exportedAt, LastChangedAt: exportedAt.Add(-time.Minute)},
		{Key: "2026-10-16", Start: day1, End: day1.AddDate(0, 0, 1), Status: StatusExported, FilePath: "x", SHA256: "c", ExportedAt: exportedAt, RowCount: 99},
		{Key: "2026-10-17", Start: day1.AddDate(0, 0, 1), End: day1.AddDate(0, 0, 2), Status: StatusOpen},
		// 读回后还没重新定稿：mgr 里是 open 但有文件，同样按 final 恢复。
		{Key: "2026-10-18", Start: day1.AddDate(0, 0, 2), End: day1.AddDate(0, 0, 3), Status: StatusOpen, FilePath: "crypto/ds_1m/2026/20261018.csv.gz", SHA256: "d",
			ExportCount: 3, RowCount: 7, SubjectCount: 1, ExportedAt: exportedAt},
	}
	n, err := s.RestorePartitions(ctx, "crypto", "ds_1m", regs, now0)
	if err != nil || n != 3 {
		t.Fatalf("inserted=%d err=%v", n, err)
	}
	p14, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-14")
	if p14.Status != StatusFinal || p14.RowCount != 10 || p14.ExportCount != 2 || p14.FilePath != regs[0].FilePath || !p14.FinalizedAt.Equal(exportedAt) || p14.ReportPending || p14.NeedsExport() {
		t.Fatalf("10-14 = %+v", p14)
	}
	p15, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if p15.Status != StatusFinal || p15.RowCount != 5 || p15.SHA256 != "b" || !p15.FinalizedAt.Equal(now0) || !p15.ExportedAt.Equal(exportedAt) || p15.ReportPending {
		t.Fatalf("登记为 exported 的应按 final 恢复: %+v", p15)
	}
	p16, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-16")
	if p16.Status != StatusOpen || p16.RowCount != 0 {
		t.Fatalf("本地已有的分区不能被登记覆盖: %+v", p16)
	}
	if _, ok, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-17"); ok {
		t.Fatal("没有文件的登记不恢复")
	}
	p18, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-18")
	if p18.Status != StatusFinal || p18.RowCount != 7 || p18.ExportCount != 3 || !p18.FinalizedAt.Equal(now0) || p18.NeedsExport() {
		t.Fatalf("有文件的 open 登记应按 final 恢复: %+v", p18)
	}
	if _, err := s.RestorePartitions(ctx, "crypto", "ds_1m", []PartitionState{{Key: "2026-10-01", Status: StatusFinal, FilePath: "x"}}, now0); err == nil {
		t.Fatal("缺少 sha256 或导出时间的登记必须报错")
	}
}

func TestStageRejectsRowsInAFinalPartition(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", day0, "", 1.0, 1.0, nil, nil)}, utcDay, now0, StageReplace)
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	_ = s.MarkExported(ctx, "crypto", "ds_1m", p.Key, now0.Add(time.Hour), "rel.csv", "sha")
	_ = s.Finalize(ctx, task, []string{p.Key}, now0)
	_, err := s.Stage(ctx, task, []Row{row("ETH", day0, "", 2.0, 2.0, nil, nil), row("ETH", day1, "", 2.0, 2.0, nil, nil)}, utcDay, now0.Add(2*time.Hour), StageReplace)
	if err == nil || !strings.Contains(err.Error(), "已定稿") {
		t.Fatalf("写入已定稿分区应报错: %v", err)
	}
	p, _, _ = s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	if _, ok, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-16"); p.Status != StatusFinal || ok {
		t.Fatalf("整批回滚: %+v other=%v", p, ok)
	}
}

func TestTimesOutsideTheNanosecondRangeAreRejected(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	task := Task{SpaceID: "crypto", DatasetID: "t", Columns: []Column{{"ts", TypeTime}}}
	_ = s.EnsureTable(ctx, task)
	for _, bad := range []time.Time{{}, time.Date(1677, 12, 31, 0, 0, 0, 0, time.UTC), time.Date(2262, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, err := s.Stage(ctx, task, []Row{row("BTC", day0, "", bad)}, utcDay, now0, StageReplace); err == nil {
			t.Fatalf("时间列 %v 应报错", bad)
		}
		if _, err := s.Stage(ctx, task, []Row{row("BTC", bad, "", nil)}, utcDay, now0, StageReplace); err == nil {
			t.Fatalf("data_time %v 应报错", bad)
		}
	}
	if _, err := s.Stage(ctx, task, []Row{row("BTC", day0, "", time.Date(2261, 12, 31, 0, 0, 0, 0, time.UTC))}, utcDay, now0, StageReplace); err != nil {
		t.Fatal(err)
	}
}

func TestResumeFromOnlyMovesForwardAndIsClearedWithTheWatermark(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	_ = s.SetFirstStart(ctx, "crypto", "ds_1m", day0)
	if err := s.SetResumeFrom(ctx, "crypto", "ds_1m", day1); err != nil {
		t.Fatal(err)
	}
	_ = s.SetResumeFrom(ctx, "crypto", "ds_1m", day0)
	if got, ok, _ := s.ResumeFrom(ctx, "crypto", "ds_1m"); !ok || !got.Equal(day1) {
		t.Fatalf("resume = %v %v", got, ok)
	}
	if got, ok, _ := s.FirstStart(ctx, "crypto", "ds_1m"); !ok || !got.Equal(day0) {
		t.Fatalf("记续读起点不能改首次起点: %v %v", got, ok)
	}
	if err := s.AdvanceWatermark(ctx, "crypto", "ds_1m", now0); err != nil {
		t.Fatal(err)
	}
	_, resume, _ := s.ResumeFrom(ctx, "crypto", "ds_1m")
	_, first, _ := s.FirstStart(ctx, "crypto", "ds_1m")
	if resume || first {
		t.Fatal("推进水位时应清掉续读起点与首次起点")
	}
}

func TestStageFillGapsWritesOnlyMissingRowsAndCountsTheRest(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	if err := s.EnsureTable(ctx, task); err != nil {
		t.Fatal(err)
	}
	at := day0.Add(time.Hour)
	if _, err := s.Stage(ctx, task, []Row{row("BTC", at, "", 1.0, 1.0, nil, nil), row("ETH", at, "", 2.0, 2.0, nil, nil)}, utcDay, now0, StageReplace); err != nil {
		t.Fatal(err)
	}
	before, _, _ := s.Partition(ctx, task.SpaceID, task.DatasetID, "2026-10-15")
	later := now0.Add(time.Hour)
	// 一行相同、一行不同：只补空缺时都不写，分区的 last_changed_at 不动。
	res, err := s.Stage(ctx, task, []Row{row("BTC", at, "", 1.0, 1.0, nil, nil), row("ETH", at, "", 9.0, 9.0, nil, nil)}, utcDay, later, StageFillGaps)
	if err != nil || res != (StageResult{Added: 0, Changed: 1, Same: 1}) {
		t.Fatalf("res = %+v, %v", res, err)
	}
	after, _, _ := s.Partition(ctx, task.SpaceID, task.DatasetID, "2026-10-15")
	if !after.LastChangedAt.Equal(before.LastChangedAt) {
		t.Fatalf("没有写入的分区不应更新 last_changed_at: %s → %s", before.LastChangedAt, after.LastChangedAt)
	}
	// 再加一行新的：只写新行，不同的那行保留原值。
	res, err = s.Stage(ctx, task, []Row{row("ETH", at, "", 9.0, 9.0, nil, nil), row("SOL", at, "", 3.0, 3.0, nil, nil)}, utcDay, later, StageFillGaps)
	if err != nil || res != (StageResult{Added: 1, Changed: 1, Same: 0}) {
		t.Fatalf("res = %+v, %v", res, err)
	}
	recent, err := s.Recent(ctx, task, "ETH", time.Time{}, 10)
	if err != nil || len(recent.Rows) != 1 || *recent.Rows[0][3] != "2.0" {
		t.Fatalf("只补空缺不应改已有行: %+v %v", recent.Rows, err)
	}
	if p, _, _ := s.Partition(ctx, task.SpaceID, task.DatasetID, "2026-10-15"); !p.LastChangedAt.Equal(later) {
		t.Fatalf("有新行写入的分区应更新 last_changed_at: %s", p.LastChangedAt)
	}
	// 比对替换：不同的行被覆盖，计数同样分三类。
	res, err = s.Stage(ctx, task, []Row{row("ETH", at, "", 9.0, 9.0, nil, nil), row("SOL", at, "", 3.0, 3.0, nil, nil)}, utcDay, later, StageReplace)
	if err != nil || res != (StageResult{Added: 0, Changed: 1, Same: 1}) {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

func TestParseCellReturnsStageValuesAndSeriesTagsAreRead(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		cell Cell
		want any
	}{
		{TypeBool, Cell{Value: "true"}, true},
		{TypeBool, Cell{Value: "0"}, false},
		{TypeTime, Cell{Value: "2026-10-15T01:02:03.5Z"}, time.Date(2026, 10, 15, 1, 2, 3, 500000000, time.UTC)},
		{TypeDouble, Cell{Value: "1.5"}, 1.5},
		{TypeInt, Cell{Value: "-3"}, int64(-3)},
		{TypeString, Cell{Value: ""}, ""},
		{TypeString, Cell{Null: true}, nil},
		{TypeBytes, Cell{Value: "aGk="}, []byte("hi")},
	} {
		got, err := ParseCell(tc.typ, tc.cell)
		if err != nil {
			t.Fatalf("%s %+v: %v", tc.typ, tc.cell, err)
		}
		if g, ok := got.([]byte); ok {
			if string(g) != string(tc.want.([]byte)) {
				t.Fatalf("%s = %v", tc.typ, got)
			}
			continue
		}
		if tm, ok := got.(time.Time); ok && !tm.Equal(tc.want.(time.Time)) || !ok && got != tc.want {
			t.Fatalf("%s %+v = %#v, want %#v", tc.typ, tc.cell, got, tc.want)
		}
	}
	for _, tc := range []struct{ typ, value string }{{TypeInt, "1.5"}, {TypeBool, "yes"}, {TypeTime, "2300-01-01T00:00:00Z"}, {TypeTime, "today"}, {TypeDouble, "x"}} {
		if _, err := ParseCell(tc.typ, Cell{Value: tc.value}); err == nil {
			t.Fatalf("%s %q 应报错", tc.typ, tc.value)
		}
	}

	// 暂存表的不同取值（取到两个就停）与归档文件里的取值。
	s, ctx, task := openTest(t), context.Background(), klineTask()
	if tags, err := s.DistinctSeriesTags(ctx, task, 2); err != nil || len(tags) != 0 {
		t.Fatalf("暂存表不存在时为空: %v %v", tags, err)
	}
	if err := s.EnsureTable(ctx, task); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Stage(ctx, task, []Row{row("BTC", day0, "a", 1.0, 1.0, nil, nil), row("BTC", day0, "b", 1.0, 1.0, nil, nil), row("BTC", day0, "c", 1.0, 1.0, nil, nil)}, utcDay, now0, StageReplace)
	if tags, err := s.DistinctSeriesTags(ctx, task, 2); err != nil || len(tags) != 2 {
		t.Fatalf("tags = %v %v", tags, err)
	}
	path := filepath.Join(t.TempDir(), "p.csv")
	_ = os.WriteFile(path, []byte("data_time,subject_id,series_tag,open\n2026-10-15T00:00:00Z,BTC,\"\",1\n2026-10-15T00:01:00Z,BTC,\"\",1\n"), 0o644)
	if tags, err := FileSeriesTags(path, 2); err != nil || len(tags) != 1 || tags[0] != "" {
		t.Fatalf("file tags = %q %v", tags, err)
	}
}

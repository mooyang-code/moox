package runner

import (
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
)

const importHeader = "data_time,subject_id,series_tag,close,open\n"

// placeImport 把 content 放到 <root>/.import/<id>/<name>（name 以 .gz 结尾时压缩），返回下发用的 import_file。
func placeImport(t *testing.T, e *env, id, name, content string) string {
	t.Helper()
	dir := filepath.Join(e.root, ".import", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if strings.HasSuffix(name, ".gz") {
		zw := gzip.NewWriter(f)
		_, _ = zw.Write([]byte(content))
		_ = zw.Close()
	} else {
		_, _ = f.WriteString(content)
	}
	return id + "/" + name
}

func (e *env) importRun(file string, overwrite bool, mutate func(*archivepb.RunAssignment)) *Outcome {
	e.t.Helper()
	// 每次导入时钟前进一分钟：真实运行之间时间总在走，导出时间与之后的写入时间才分得开。
	e.now = e.now.Add(time.Minute)
	return e.run(2000, func(a *archivepb.RunAssignment) {
		a.Kind, a.ImportFile, a.Overwrite = "import", file, overwrite
		if mutate != nil {
			mutate(a)
		}
	}, nil)
}

// 2026-09-01 早已超出 168h 保留期，按“开始 ≥ 收盘 + L + 暂存保留期”当场定稿。
var historyDay = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

func historyCSV(lines ...string) string { return importHeader + strings.Join(lines, "\n") + "\n" }

func requireNoReadState(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	if _, ok, _ := e.store.Watermark(ctx, "crypto", "ds_1m"); ok {
		t.Fatal("导入不应推进水位")
	}
	if _, ok, _ := e.store.FirstStart(ctx, "crypto", "ds_1m"); ok {
		t.Fatal("导入不应写首次起点")
	}
	if _, ok, _ := e.store.ResumeFrom(ctx, "crypto", "ds_1m"); ok {
		t.Fatal("导入不应写续读起点")
	}
	if len(e.src.requests) != 0 {
		t.Fatalf("导入不应读 Storage: %v", e.src.requests)
	}
}

func TestImportWritesHistoricalPartitionsAndFinalizesThemOnTheSpot(t *testing.T) {
	e := newEnv(t)
	file := placeImport(t, e, "imp_1", "a.csv.gz", historyCSV(
		"2026-09-01T00:00:00Z,BTC,,1.5,1",
		"2026-09-01T00:00:00Z,ETH,,10.5,10",
		"2026-09-01T00:01:00Z,BTC,,2.5,2",
		"2026-09-02T00:00:00Z,BTC,,3.5,",
	))
	out := e.importRun(file, false, nil)
	if out.Status != "succeeded" || out.RowsAdded != 4 || out.Progress.RowsRead != 4 || out.Progress.SubjectsDone != 2 || out.Exported != 2 || out.Finalized != 2 {
		t.Fatalf("out = %+v", out)
	}
	if out.Message != "导入 4 行（新增 4、已存在相同 0、已存在不同 0 未覆盖），涉及 2 个分区" {
		t.Fatalf("message = %q", out.Message)
	}
	p := report(out, "2026-09-01")
	if p.GetStatus() != "final" || p.GetRowCount() != 3 || p.GetFilePath() != "crypto/collect/kline-1m/2026/20260901.csv" {
		t.Fatalf("partition = %v", p)
	}
	content := readFile(t, e.root, p.GetFilePath())
	if !strings.Contains(content, "2026-09-01T00:00:00Z,ETH,\"\",10.0,10.5") {
		t.Fatalf("file = %s", content)
	}
	if !strings.Contains(readFile(t, e.root, report(out, "2026-09-02").GetFilePath()), "2026-09-02T00:00:00Z,BTC,\"\",,3.5\n") {
		t.Fatal("空字段应写成缺失值")
	}
	if len(out.Unrecoverable) != 0 {
		t.Fatalf("导入不做保留期截断: %v", out.Unrecoverable)
	}
	requireNoReadState(t, e)
	// 导入目录由 App 在 mgr 确认成功之后删除，runner 不动它。
	if _, err := os.Stat(filepath.Join(e.root, ".import", "imp_1")); err != nil {
		t.Fatalf("runner 不应删除导入目录: %v", err)
	}
}

func TestImportFillsGapsByDefaultAndOverwritesOnlyWhenAsked(t *testing.T) {
	e := newEnv(t)
	base := []string{"2026-09-01T00:00:00Z,BTC,,1.5,1", "2026-09-01T00:01:00Z,BTC,,2.5,2", "2026-09-01T00:02:00Z,BTC,,3.5,3"}
	out := e.importRun(placeImport(t, e, "imp_1", "a.csv", historyCSV(base...)), false, nil)
	first := report(out, "2026-09-01")
	ctx := context.Background()

	// 全部相同：读回已定稿分区比对，没有写入，文件与导出次数都不变，分区重新定稿。
	out = e.importRun(placeImport(t, e, "imp_2", "a.csv", historyCSV(base...)), false, nil)
	if out.Status != "succeeded" || !strings.Contains(out.Message, "新增 0、已存在相同 3、已存在不同 0 未覆盖") || out.Exported != 0 {
		t.Fatalf("out = %+v", out)
	}
	state := partitionState(t, e, "2026-09-01")
	if state.Status != staging.StatusFinal || state.SHA256 != first.GetFileSha256() || state.ExportCount != first.GetExportCount() {
		t.Fatalf("全部相同时不应重新导出: %+v", state)
	}

	// 一行不同、一行新增：只补空缺，不同的那行保留原值。
	changed := []string{"2026-09-01T00:00:00Z,BTC,,9.5,9", "2026-09-01T00:01:00Z,BTC,,2.5,2", "2026-09-01T00:03:00Z,BTC,,4.5,4"}
	out = e.importRun(placeImport(t, e, "imp_3", "a.csv", historyCSV(changed...)), false, nil)
	if !strings.Contains(out.Message, "导入 3 行（新增 1、已存在相同 1、已存在不同 1 未覆盖），涉及 1 个分区") || out.RowsAdded != 1 || out.RowsChanged != 0 {
		t.Fatalf("out = %+v", out)
	}
	content := readFile(t, e.root, first.GetFilePath())
	if !strings.Contains(content, "2026-09-01T00:00:00Z,BTC,\"\",1.0,1.5") || !strings.Contains(content, "00:03:00Z,BTC,\"\",4.0,4.5") {
		t.Fatalf("只补空缺: %s", content)
	}

	// 覆盖：不同的那行改成新值。
	out = e.importRun(placeImport(t, e, "imp_4", "a.csv", historyCSV(changed...)), true, nil)
	if !strings.Contains(out.Message, "新增 0、已存在相同 2、已存在不同 1 已覆盖") || out.RowsChanged != 1 {
		t.Fatalf("out = %+v", out)
	}
	if content := readFile(t, e.root, first.GetFilePath()); !strings.Contains(content, "2026-09-01T00:00:00Z,BTC,\"\",9.0,9.5") {
		t.Fatalf("覆盖后: %s", content)
	}
	if p := partitionState(t, e, "2026-09-01"); p.Status != staging.StatusFinal {
		t.Fatalf("历史分区应当场重新定稿: %+v", p)
	}
	_ = ctx
}

// 只补空缺时没有实际写入的分区不更新 last_changed_at（open 的分区，不经读回）。
func TestFillGapsLeavesUntouchedPartitionsAlone(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.src.rows = rows("BTC", day, 3, 1)
	e.run(2000, nil, nil)
	before := partitionState(t, e, "2026-10-17")
	watermark := e.now
	e.src.requests = nil
	out := e.importRun(placeImport(t, e, "imp_1", "a.csv", "data_time,subject_id,close,open\n2026-10-17T00:00:00Z,BTC,7,7\n"), false, nil)
	if out.Status != "succeeded" || !strings.Contains(out.Message, "已存在不同 1 未覆盖") {
		t.Fatalf("out = %+v", out)
	}
	after := partitionState(t, e, "2026-10-17")
	if !after.LastChangedAt.Equal(before.LastChangedAt) {
		t.Fatalf("没有写入的分区不应更新 last_changed_at: %s → %s", before.LastChangedAt, after.LastChangedAt)
	}
	if w, ok, _ := e.store.Watermark(context.Background(), "crypto", "ds_1m"); !ok || !w.Equal(watermark) {
		t.Fatalf("导入不应改动已有水位: %v %v", w, ok)
	}
}

func TestImportRejectsTheWholeFileOnAnyError(t *testing.T) {
	for name, tc := range map[string]struct {
		content   string
		overwrite bool
		want      string
	}{
		"未知列":    {"data_time,subject_id,series_tag,close,volume\n2026-09-01T00:00:00Z,BTC,,1,1\n", false, "数据集没有的列：volume"},
		"缺主键列":   {"data_time,series_tag,close\n2026-09-01T00:00:00Z,,1\n", false, "data_time 与 subject_id"},
		"重复列":    {"data_time,subject_id,close,close\n", false, "重复"},
		"覆盖缺列":   {"data_time,subject_id,series_tag,close\n2026-09-01T00:00:00Z,BTC,,1\n", true, "缺少：open"},
		"没对齐":    {historyCSV("2026-09-01T00:00:30Z,BTC,,1,1"), false, "第 2 行：data_time 2026-09-01T00:00:30Z 没有按频率 1m 对齐"},
		"乱序":     {historyCSV("2026-09-01T00:01:00Z,BTC,,1,1", "2026-09-01T00:00:00Z,BTC,,1,1"), false, "第 3 行：data_time 2026-09-01T00:00:00Z 早于上一行"},
		"类型":     {historyCSV("2026-09-01T00:00:00Z,BTC,,abc,1"), false, "列 close 不是合法的 double 值"},
		"时间格式":   {historyCSV("2026/09/01,BTC,,1,1"), false, "data_time 不是 RFC3339 时间"},
		"重复键":    {historyCSV("2026-09-01T00:00:00Z,BTC,,1,1", "2026-09-01T00:00:00Z,ETH,,1,1", "2026-09-01T00:00:00Z,BTC,,2,2"), false, "第 4 行：与第 2 行的主键重复"},
		"字段数":    {historyCSV("2026-09-01T00:00:00Z,BTC,,1"), false, "有 4 个字段，表头是 5 列"},
		"只有表头":   {importHeader, false, "只有表头"},
		"空文件":    {"", false, "空的"},
		"没有可参照的": {"data_time,subject_id,close,open\n2026-09-01T00:00:00Z,BTC,1,1\n", false, "必须带 series_tag 列"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			out := e.importRun(placeImport(t, e, "imp_1", "a.csv", tc.content), tc.overwrite, nil)
			if out.Status != "failed" || !strings.Contains(out.Message, tc.want) {
				t.Fatalf("out = %s %q", out.Status, out.Message)
			}
			parts, _ := e.store.Partitions(context.Background(), "crypto", "ds_1m")
			if len(parts) != 0 || out.RowsAdded != 0 {
				t.Fatalf("有错时什么都不写: %v", parts)
			}
			if _, err := os.Stat(filepath.Join(e.root, ".import", "imp_1", "a.csv")); err != nil {
				t.Fatalf("失败时保留导入文件: %v", err)
			}
		})
	}
}

func TestImportListsAtMostTwentyErrors(t *testing.T) {
	e := newEnv(t)
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("2026-09-01T00:%02d:30Z,BTC,,1,1", i))
	}
	out := e.importRun(placeImport(t, e, "imp_1", "a.csv", historyCSV(lines...)), false, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, "有 30 处错误") || !strings.Contains(out.Message, "只列前 20 条") ||
		strings.Count(out.Message, "没有按频率") != 20 || !strings.Contains(out.Message, "第 21 行") || strings.Contains(out.Message, "第 22 行") {
		t.Fatalf("message = %q", out.Message)
	}
}

func TestImportSeriesTagIsFilledFromTheDatasetOrMustMatch(t *testing.T) {
	e := newEnv(t)
	e.importRun(placeImport(t, e, "imp_1", "a.csv", historyCSV("2026-09-01T00:00:00Z,BTC,spot,1,1")), false, nil)
	// 暂存行已在定稿时删掉：从最近一个分区文件读到唯一的取值 spot，缺列时自动补。
	out := e.importRun(placeImport(t, e, "imp_2", "a.csv", "data_time,subject_id,close,open\n2026-09-02T00:00:00Z,BTC,2,2\n"), false, nil)
	if out.Status != "succeeded" || !strings.Contains(readFile(t, e.root, report(out, "2026-09-02").GetFilePath()), "2026-09-02T00:00:00Z,BTC,spot,2.0,2.0") {
		t.Fatalf("out = %+v", out)
	}
	// 填了不一样的值、或带列但留空：前者拒绝，后者自动补。
	out = e.importRun(placeImport(t, e, "imp_3", "a.csv", historyCSV("2026-09-03T00:00:00Z,BTC,perp,1,1")), false, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, `series_tag "perp" 与数据集的 "spot" 不一致`) {
		t.Fatalf("out = %+v", out)
	}
	out = e.importRun(placeImport(t, e, "imp_4", "a.csv", "data_time,subject_id,series_tag,close,open\n2026-09-03T00:00:00Z,BTC,,1,1\n"), false, nil)
	if out.Status != "succeeded" {
		t.Fatalf("空值也补数据集的取值: %+v", out)
	}

	// 暂存表里有两个取值：缺列时拒绝。
	multi := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	multi.src.rows = []staging.Row{{SubjectID: "BTC", DataTime: day, SeriesTag: "a", Values: []any{1.0, 1.0}}, {SubjectID: "BTC", DataTime: day, SeriesTag: "b", Values: []any{1.0, 1.0}}}
	multi.run(2000, nil, nil)
	multi.src.requests = nil
	out = multi.importRun(placeImport(t, multi, "imp_1", "a.csv", "data_time,subject_id,close,open\n2026-09-01T00:00:00Z,BTC,1,1\n"), false, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, "不止一个取值") {
		t.Fatalf("out = %+v", out)
	}
}

// 暂存库丢失后，导入先按下发的全部登记补齐本地分区状态，落在已定稿分区的行读回原文件再合并。
func TestImportRestoresRegistrationsBeforeWritingIntoAnOldPartition(t *testing.T) {
	e := newEnv(t)
	out := e.importRun(placeImport(t, e, "imp_1", "a.csv", historyCSV("2026-09-01T00:00:00Z,BTC,,1,1")), false, nil)
	registered := report(out, "2026-09-01")
	_ = e.store.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(e.root, ".state", "staging.db"+suffix))
	}
	store, err := staging.Open(filepath.Join(e.root, ".state", "staging.db"), staging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e.store = store
	out = e.importRun(placeImport(t, e, "imp_2", "a.csv", historyCSV("2026-09-01T00:01:00Z,BTC,,2,2")), false, func(a *archivepb.RunAssignment) {
		a.RegisteredPartitions = []*archivepb.PartitionReport{registered}
	})
	p := report(out, "2026-09-01")
	if out.Status != "succeeded" || p.GetRowCount() != 2 || p.GetExportCount() != 2 || p.GetStatus() != "final" {
		t.Fatalf("out = %+v partition = %v", out, p)
	}
}

// 大文件：两遍都按行数上报进度（App 的定时器据此续租）；分块写入不影响结果。
func TestLargeImportReportsProgressInBothPassesAndCanBeCanceled(t *testing.T) {
	previous := importChunkRows
	importChunkRows = 7
	t.Cleanup(func() { importChunkRows = previous })
	e := newEnv(t)
	var lines []string
	for i := 0; i < 12000; i++ {
		at := historyDay.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		lines = append(lines, at+",BTC,,1,1", at+",ETH,,2,2")
	}
	file := placeImport(t, e, "imp_1", "a.csv", historyCSV(lines...))
	var reports []Progress
	out := e.run(2000, func(a *archivepb.RunAssignment) { a.Kind, a.ImportFile = "import", file }, func(p Progress) bool {
		reports = append(reports, p)
		return false
	})
	if out.Status != "succeeded" || out.RowsAdded != 24000 {
		t.Fatalf("out = %s %q", out.Status, out.Message)
	}
	if len(reports) != 4 || reports[0].RowsRead != 10000 || reports[2].RowsRead != 10000 {
		t.Fatalf("两遍各每 1 万行上报一次: %+v", reports)
	}
	canceled := newEnv(t)
	file = placeImport(t, canceled, "imp_1", "a.csv", historyCSV(lines...))
	out = canceled.run(2000, func(a *archivepb.RunAssignment) { a.Kind, a.ImportFile = "import", file }, func(Progress) bool { return true })
	if out.Status != "canceled" {
		t.Fatalf("out = %+v", out)
	}
}

func TestImportPathOnlyAcceptsTheImportDirectory(t *testing.T) {
	for _, bad := range []string{"", "a.csv", "../a.csv", "imp/../a.csv", "imp/.a.csv", "./a.csv", "imp/sub/a.csv", ".state/staging.db"} {
		if _, _, err := ImportPath("/data/archive", bad); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
	dir, path, err := ImportPath("/data/archive", "imp_1/a.csv")
	if err != nil || dir != "/data/archive/.import/imp_1" || path != "/data/archive/.import/imp_1/a.csv" {
		t.Fatalf("%s %s %v", dir, path, err)
	}
	e := newEnv(t)
	out := e.importRun("../etc/a.csv", false, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, "无效") {
		t.Fatalf("out = %+v", out)
	}
}

// 分块写入时每块之后就导出、定稿已经写完的分区：暂存行不随文件线性增长。
func TestImportSettlesFinishedPartitionsAfterEveryChunk(t *testing.T) {
	previous := importChunkRows
	importChunkRows = 100
	t.Cleanup(func() { importChunkRows, importChunkHook = previous, nil })
	e := newEnv(t)
	var lines []string
	for i := 0; i < 5*1440/10; i++ { // 5 天，每 10 分钟一行
		lines = append(lines, historyDay.Add(time.Duration(i)*10*time.Minute).Format(time.RFC3339)+",BTC,,1,1")
	}
	file := placeImport(t, e, "imp_1", "a.csv", historyCSV(lines...))
	chunks := 0
	all := staging.PartitionState{Start: historyDay.AddDate(-1, 0, 0), End: historyDay.AddDate(1, 0, 0)}
	importChunkHook = func(s *runState, last time.Time) error {
		chunks++
		parts, err := s.r.Staging.Partitions(context.Background(), "crypto", "ds_1m")
		if err != nil {
			return err
		}
		for _, p := range parts {
			if !p.End.After(last) && p.Status != staging.StatusFinal {
				t.Errorf("第 %d 块之后，已写完的分区 %s 应已定稿: %s", chunks, p.Key, p.Status)
			}
		}
		st, err := s.r.Staging.Stats(context.Background(), s.task, all)
		if err != nil {
			return err
		}
		// 暂存行只剩还没写完的那个分区（一天 144 行）里的。
		if st.Rows > 144 {
			t.Errorf("第 %d 块之后暂存 %d 行，应只剩当前分区的", chunks, st.Rows)
		}
		return nil
	}
	out := e.importRun(file, false, nil)
	if out.Status != "succeeded" || out.Finalized != 5 || out.RowsAdded != 720 || chunks < 7 {
		t.Fatalf("out = %s %q finalized=%d chunks=%d", out.Status, out.Message, out.Finalized, chunks)
	}
}

// 第二遍中途出错时，消息写明已经写入了多少行、怎样补齐。
func TestImportFailureInTheSecondPassTellsHowManyRowsWereWritten(t *testing.T) {
	previous := importChunkRows
	importChunkRows = 2
	t.Cleanup(func() { importChunkRows, importChunkHook = previous, nil })
	e := newEnv(t)
	file := placeImport(t, e, "imp_1", "a.csv", historyCSV("2026-09-01T00:00:00Z,BTC,,1,1", "2026-09-01T00:01:00Z,BTC,,1,1", "2026-09-01T00:02:00Z,BTC,,1,1", "2026-09-01T00:03:00Z,BTC,,1,1"))
	importChunkHook = func(*runState, time.Time) error { return fmt.Errorf("磁盘写满") }
	out := e.importRun(file, false, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, "磁盘写满（已写入前 2 行；可用同一文件不带 --overwrite 重新导入补齐") {
		t.Fatalf("out = %q", out.Message)
	}
	out = e.importRun(placeImport(t, e, "imp_2", "a.csv", historyCSV("2026-09-02T00:00:00Z,BTC,,1,1", "2026-09-02T00:01:00Z,BTC,,1,1", "2026-09-02T00:02:00Z,BTC,,1,1")), true, nil)
	if !strings.Contains(out.Message, "可用同一文件再次 --overwrite 导入") {
		t.Fatalf("out = %q", out.Message)
	}
}

func TestImportHeaderBOMTrailingBlankLinesAndLongRecords(t *testing.T) {
	e := newEnv(t)
	out := e.importRun(placeImport(t, e, "imp_1", "a.csv", "\ufeff"+historyCSV("2026-09-01T00:00:00Z,BTC,,1,1")+"\n\n"), false, nil)
	if out.Status != "succeeded" || out.RowsAdded != 1 {
		t.Fatalf("表头的 BOM 与末尾空行应忽略: %s %q", out.Status, out.Message)
	}
	out = e.importRun(placeImport(t, e, "imp_2", "a.csv", historyCSV("2026-09-01T00:01:00Z,BTC,,1,1", "", "2026-09-01T00:02:00Z,BTC,,1,1")), false, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, "第 3 行：空行") {
		t.Fatalf("中间的空行算错误: %q", out.Message)
	}
	long := historyCSV("2026-09-01T00:03:00Z,BTC," + strings.Repeat("x", staging.MaxImportRecordBytes) + ",1,1")
	out = e.importRun(placeImport(t, e, "imp_3", "a.csv", long), false, nil)
	if out.Status != "failed" || !strings.Contains(out.Message, "第 2 行：记录过长（超过 1MiB）") {
		t.Fatalf("out = %q", out.Message)
	}
}

// 数据集已有多个 series_tag 时只要求文件带这一列，不校验取值。
func TestImportWithManySeriesTagsOnlyNeedsTheColumn(t *testing.T) {
	e := newEnv(t)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	e.src.rows = []staging.Row{{SubjectID: "BTC", DataTime: day, SeriesTag: "a", Values: []any{1.0, 1.0}}, {SubjectID: "BTC", DataTime: day, SeriesTag: "b", Values: []any{1.0, 1.0}}}
	e.run(2000, nil, nil)
	e.src.requests = nil
	out := e.importRun(placeImport(t, e, "imp_1", "a.csv", historyCSV("2026-09-01T00:00:00Z,BTC,c,1,1")), false, nil)
	if out.Status != "succeeded" || out.RowsAdded != 1 {
		t.Fatalf("out = %s %q", out.Status, out.Message)
	}
}

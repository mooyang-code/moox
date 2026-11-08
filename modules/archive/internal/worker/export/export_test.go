package export

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
)

func TestRelPathBuildsDayAndMonthNamesAndRejectsUnsafeInput(t *testing.T) {
	for _, tc := range []struct{ space, purpose, dir, key, want string }{
		{"crypto", "collect", "binance-spot-kline-1m", "2026-10-15", "crypto/collect/binance-spot-kline-1m/2026/20261015.csv"},
		{"stockcn", "factor", "hs300-1d", "2026-10", "stockcn/factor/hs300-1d/2026/202610.csv"},
	} {
		got, err := RelPath(tc.space, tc.purpose, tc.dir, tc.key, FormatCSV)
		if err != nil || got != tc.want {
			t.Fatalf("RelPath(%v) = %q, %v, want %q", tc, got, err, tc.want)
		}
	}
	if got, _ := RelPath("crypto", "collect", "binance-spot-kline-1m", "2026-10-15", FormatCSVGz); got != "crypto/collect/binance-spot-kline-1m/2026/20261015.csv.gz" {
		t.Fatalf("csv.gz 路径 = %q", got)
	}
	task := staging.Task{SpaceID: "crypto", DatasetID: "ds_1m", Purpose: "factor", DirName: "binance-spot-1m"}
	if got, _ := (Files{}).RelPath(task, "2026-10"); got != "crypto/factor/binance-spot-1m/2026/202610.csv.gz" {
		t.Fatalf("默认格式应是 csv.gz: %q", got)
	}
	if got, err := DatasetJSONPath("crypto", "collect", "binance-spot-kline-1m"); err != nil || got != "crypto/collect/binance-spot-kline-1m/dataset.json" {
		t.Fatalf("dataset.json 路径 = %q, %v", got, err)
	}
	for _, tc := range []struct{ space, purpose, dir, key string }{
		{"crypto", "collect", "d", "2026-1"}, {"crypto", "collect", "d", "20261015"}, {"crypto", "collect", "d", "2026-10-1x"},
		{"../etc", "collect", "d", "2026-10"}, {"crypto", "a/b", "d", "2026-10"}, {"crypto", "collect", "..", "2026-10"},
		{".state", "collect", "d", "2026-10"}, {"crypto", ".import", "d", "2026-10"}, {"", "collect", "d", "2026-10"},
		{"crypto", "", "d", "2026-10"}, {"crypto", "collect", "", "2026-10"},
	} {
		if _, err := RelPath(tc.space, tc.purpose, tc.dir, tc.key, FormatCSV); err == nil {
			t.Fatalf("RelPath(%v) 应报错", tc)
		}
	}
}

func setup(t *testing.T) (Files, *staging.Store, staging.Task, staging.PartitionState) {
	t.Helper()
	root := t.TempDir()
	store, err := staging.Open(filepath.Join(root, ".state", "staging.db"), staging.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	task := staging.Task{SpaceID: "crypto", DatasetID: "ds_1m", Purpose: "collect", DirName: "binance-spot-kline-1m", Columns: []staging.Column{{Name: "open", Type: staging.TypeDouble}}}
	if err := store.EnsureTable(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	ref := func(time.Time) staging.PartitionRef {
		return staging.PartitionRef{Key: "2026-10-15", Start: day, End: day.AddDate(0, 0, 1)}
	}
	var rows []staging.Row
	for i := 0; i < 40; i++ {
		rows = append(rows, staging.Row{SubjectID: "BTC", DataTime: day.Add(time.Duration(i) * time.Minute), SeriesTag: "", Values: []any{float64(i)}})
	}
	if _, err := store.Stage(context.Background(), task, rows, ref, day.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	p, ok, err := store.Partition(context.Background(), "crypto", "ds_1m", "2026-10-15")
	if err != nil || !ok {
		t.Fatalf("partition: %v %v", ok, err)
	}
	return Files{Root: root, Format: FormatCSVGz}, store, task, p
}

func TestWriteIsAtomicAndReportsHashAndPreview(t *testing.T) {
	files, store, task, p := setup(t)
	info, err := files.Write(context.Background(), store, task, p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != "crypto/collect/binance-spot-kline-1m/2026/20261015.csv.gz" || len(info.SHA256) != 64 {
		t.Fatalf("info = %+v", info)
	}
	lines := strings.Split(strings.TrimSuffix(info.Preview, "\n"), "\n")
	if len(lines) != 21 || lines[0] != "data_time,subject_id,series_tag,open" || !strings.HasPrefix(lines[1], "2026-10-15T00:00:00Z,BTC,\"\",0.0") {
		t.Fatalf("preview = %q", info.Preview)
	}
	// 写入时算出的 sha256 是磁盘上（压缩后）字节的哈希。
	if err := files.Verify(info.Path, info.SHA256); err != nil {
		t.Fatal(err)
	}
	if err := files.Verify(info.Path, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("sha256 不一致必须报错: %v", err)
	}
	if err := files.Verify("crypto/missing.csv", info.SHA256); err == nil {
		t.Fatal("文件缺失必须报错")
	}
	if _, err := os.Stat(files.Abs(info.Path) + ".partial"); !os.IsNotExist(err) {
		t.Fatalf(".partial 不应残留: %v", err)
	}
	f, _ := os.Open(files.Abs(info.Path))
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	_ = f.Close()
	if strings.Count(string(raw), "\n") != 41 {
		t.Fatalf("行数 = %d", strings.Count(string(raw), "\n"))
	}
	// 本地没有导出记录、目标文件却已存在（例如删除任务后用同一目录名重建）：拒绝覆盖，并说明怎么处理。
	if _, err := files.Write(context.Background(), store, task, p); err == nil || !strings.Contains(err.Error(), "旧任务留下的文件") || !strings.Contains(err.Error(), "换一个目录名") {
		t.Fatalf("目标文件已存在且本地无记录时应拒绝导出: %v", err)
	}
	// 有记录时重写：同路径原子替换。
	p.FilePath = info.Path
	again, err := files.Write(context.Background(), store, task, p)
	if err != nil || again.Path != info.Path || again.SHA256 != info.SHA256 {
		t.Fatalf("again = %+v err=%v", again, err)
	}
}

// 已有文件的分区重写时沿用记录的路径与格式，不按当前配置另写一份。
func TestRewriteKeepsTheRecordedPathAndFormat(t *testing.T) {
	files, store, task, p := setup(t)
	files.Format = FormatCSV
	info, err := files.Write(context.Background(), store, task, p)
	if err != nil || info.Path != "crypto/collect/binance-spot-kline-1m/2026/20261015.csv" {
		t.Fatalf("info = %+v err=%v", info, err)
	}
	files.Format = FormatCSVGz
	p.FilePath = info.Path
	again, err := files.Write(context.Background(), store, task, p)
	if err != nil || again.Path != info.Path {
		t.Fatalf("again = %+v err=%v", again, err)
	}
	raw, _ := os.ReadFile(files.Abs(again.Path))
	if !strings.HasPrefix(string(raw), "data_time,") {
		t.Fatal("沿用原格式（未压缩）")
	}
	entries, _ := os.ReadDir(filepath.Dir(files.Abs(info.Path)))
	if len(entries) != 1 {
		t.Fatalf("目录里该分区只应有一份文件: %v", entries)
	}
}

func TestWriteFailureLeavesNoPartialAndKeepsTheOldFile(t *testing.T) {
	files, store, task, p := setup(t)
	info, err := files.Write(context.Background(), store, task, p)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(files.Abs(info.Path))
	p.FilePath = info.Path
	bad := task
	bad.Columns = []staging.Column{{Name: "no_such_column", Type: staging.TypeDouble}} // 暂存表里没有这一列：COPY 会失败
	if _, err := files.Write(context.Background(), store, bad, p); err == nil {
		t.Fatal("导出失败必须返回错误")
	}
	after, _ := os.ReadFile(files.Abs(info.Path))
	if string(before) != string(after) {
		t.Fatal("导出失败不能改动已有的正式文件")
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(files.Abs(info.Path)), "*.partial"))
	if len(matches) != 0 {
		t.Fatalf("残留 .partial: %v", matches)
	}
	unsafe := task
	unsafe.DirName = "../x"
	p.FilePath = ""
	if _, err := files.Write(context.Background(), store, unsafe, p); err == nil {
		t.Fatal("不安全的路径必须被拒绝")
	}
}

func TestDatasetJSONCarriesTheDatasetAndColumnDescriptionsAndIsRewrittenOnlyOnRealChanges(t *testing.T) {
	files := Files{Root: t.TempDir()}
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	cols := []staging.Column{{Name: "open", Type: staging.TypeDouble}}
	input := DatasetMetaInput{
		SpaceID: "crypto", DatasetID: "ds_1m", Purpose: "collect", DirName: "binance-spot-kline-1m", Freq: "1m", PartitionUnit: "day", Timezone: "UTC",
		DatasetJSON: `{"name":"现货分钟K线","description":"币安现货 1 分钟 K 线"}`, Columns: cols,
		Infos: []ColumnMeta{{Name: "open", DisplayName: "开盘价", OriginType: "field", OriginID: "open"}},
	}
	if err := files.WriteDatasetJSON(files.BuildDatasetMeta(input, now)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(files.Root, "crypto", "collect", "binance-spot-kline-1m", "dataset.json")
	first, _ := os.ReadFile(path)
	for _, want := range []string{`"purpose": "collect"`, `"dir_name": "binance-spot-kline-1m"`, `"name": "现货分钟K线"`, `"description": "币安现货 1 分钟 K 线"`, `"display_name": "开盘价"`, `"display_name": "数据时间"`, `"path_pattern": "<yyyy>/<yyyymmdd>.csv.gz"`, `"format": "csv.gz"`, `"timezone": "UTC"`} {
		if !strings.Contains(string(first), want) {
			t.Fatalf("元数据缺少 %s:\n%s", want, first)
		}
	}
	if err := files.WriteDatasetJSON(files.BuildDatasetMeta(input, now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(path); string(again) != string(first) {
		t.Fatal("内容没变不应重写")
	}
	input.DatasetJSON = `{"name":"现货分钟K线","description":"新的简介"}`
	if err := files.WriteDatasetJSON(files.BuildDatasetMeta(input, now.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	changed, _ := os.ReadFile(path)
	if string(changed) == string(first) || !strings.Contains(string(changed), "新的简介") {
		t.Fatalf("简介变化应重写: %s", changed)
	}
}

func TestCleanPartialsRemovesLeftoversButNotTheStateDirectory(t *testing.T) {
	root := t.TempDir()
	files := Files{Root: root}
	keep := filepath.Join(root, ".state", "staging.db.partial")
	stale := filepath.Join(root, "crypto", "ds", "2026", "ds_20261015.csv.gz.partial")
	good := filepath.Join(root, "crypto", "ds", "2026", "ds_20261014.csv.gz")
	for _, p := range []string{keep, stale, good} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := files.CleanPartials()
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d err=%v", removed, err)
	}
	for _, p := range []string{keep, good} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s 不应被删除: %v", p, err)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("残留的 .partial 应被清理")
	}
	if n, err := (Files{Root: filepath.Join(root, "missing")}).CleanPartials(); err != nil || n != 0 {
		t.Fatalf("目录不存在: n=%d err=%v", n, err)
	}
}

func TestWritePlainCSV(t *testing.T) {
	files, store, task, p := setup(t)
	files.Format = FormatCSV
	info, err := files.Write(context.Background(), store, task, p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != "crypto/collect/binance-spot-kline-1m/2026/20261015.csv" || !strings.HasPrefix(info.Preview, "data_time,subject_id,series_tag,open\n") {
		t.Fatalf("info = %+v", info)
	}
	raw, err := os.ReadFile(files.Abs(info.Path))
	if err != nil || !strings.HasPrefix(string(raw), "data_time,subject_id,series_tag,open\n") {
		t.Fatalf("文件应是未压缩的 CSV: %v %q", err, raw[:20])
	}
	if err := files.Verify(info.Path, info.SHA256); err != nil {
		t.Fatal(err)
	}
}

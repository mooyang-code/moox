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
	for _, tc := range []struct{ space, dataset, key, want string }{
		{"crypto", "ds_1m", "2026-10-15", "crypto/2026/ds_1m_20261015.csv"},
		{"stockcn", "cn_1d", "2026-10", "stockcn/2026/cn_1d_202610.csv"},
		{"crypto", "币安现货1分钟K线", "2026-10-15", "crypto/2026/币安现货1分钟K线_20261015.csv"},
	} {
		got, err := RelPath(tc.space, tc.dataset, tc.key, FormatCSV)
		if err != nil || got != tc.want {
			t.Fatalf("RelPath(%v) = %q, %v, want %q", tc, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ space, dataset, key string }{
		{"crypto", "ds", "2026-1"}, {"crypto", "ds", "20261015"}, {"crypto", "ds", "2026-10-1x"},
		{"../etc", "ds", "2026-10"}, {"crypto", "a/b", "2026-10"}, {"crypto", "..", "2026-10"}, {".state", "ds", "2026-10"}, {"", "ds", "2026-10"},
	} {
		if _, err := RelPath(tc.space, tc.dataset, tc.key, FormatCSV); err == nil {
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
	task := staging.Task{SpaceID: "crypto", DatasetID: "ds_1m", Columns: []staging.Column{{Name: "open", Type: staging.TypeDouble}}}
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

func TestWriteIsAtomicAndReportsHashSizeAndPreview(t *testing.T) {
	files, store, task, p := setup(t)
	info, err := files.Write(context.Background(), store, task, "ds_1m", p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != "crypto/2026/ds_1m_20261015.csv.gz" || info.Bytes == 0 || len(info.SHA256) != 64 {
		t.Fatalf("info = %+v", info)
	}
	lines := strings.Split(strings.TrimSuffix(info.Preview, "\n"), "\n")
	if len(lines) != 21 || lines[0] != "data_time,subject_id,series_tag,open" || !strings.HasPrefix(lines[1], "2026-10-15T00:00:00Z,BTC,\"\",0.0") {
		t.Fatalf("preview = %q", info.Preview)
	}
	if sha, size, err := FileSHA256(files.Abs(info.Path)); err != nil || sha != info.SHA256 || size != info.Bytes {
		t.Fatalf("sha=%s size=%d err=%v", sha, size, err)
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
	// 读完立刻关闭：Windows 上不能把仍被打开的文件作为重命名的目标替换。
	_ = f.Close()
	if strings.Count(string(raw), "\n") != 41 {
		t.Fatalf("行数 = %d", strings.Count(string(raw), "\n"))
	}
	// 重写：同路径原子替换。
	again, err := files.Write(context.Background(), store, task, "ds_1m", p)
	if err != nil || again.Path != info.Path {
		t.Fatalf("again = %+v err=%v", again, err)
	}
}

func TestWriteFailureLeavesNoPartialAndKeepsTheOldFile(t *testing.T) {
	files, store, task, p := setup(t)
	info, err := files.Write(context.Background(), store, task, "ds_1m", p)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(files.Abs(info.Path))
	bad := task
	bad.Columns = []staging.Column{{Name: "no_such_column", Type: staging.TypeDouble}} // 暂存表里没有这一列：COPY 会失败
	if _, err := files.Write(context.Background(), store, bad, "ds_1m", p); err == nil {
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
	unsafe.DatasetID = "../x"
	if _, err := files.Write(context.Background(), store, unsafe, "ds_1m", p); err == nil {
		t.Fatal("不安全的路径必须被拒绝")
	}
}

func TestDatasetJSONCarriesTheDatasetAndColumnDescriptionsAndIsRewrittenOnlyOnRealChanges(t *testing.T) {
	files := Files{Root: t.TempDir()}
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	cols := []staging.Column{{Name: "open", Type: staging.TypeDouble}}
	input := DatasetMetaInput{
		SpaceID: "crypto", DatasetID: "ds_1m", FilePrefix: "现货分钟K线", Freq: "1m", PartitionUnit: "day", Timezone: "UTC",
		DatasetJSON: `{"name":"现货分钟K线","description":"币安现货 1 分钟 K 线"}`, Columns: cols,
		Infos: []ColumnMeta{{Name: "open", DisplayName: "开盘价", OriginType: "field", OriginID: "open"}},
	}
	if err := files.WriteDatasetJSON(files.BuildDatasetMeta(input, now)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(files.Root, "crypto", "现货分钟K线_dataset.json")
	first, _ := os.ReadFile(path)
	for _, want := range []string{`"name": "现货分钟K线"`, `"description": "币安现货 1 分钟 K 线"`, `"display_name": "开盘价"`, `"display_name": "数据时间"`, `"path_pattern": "<yyyy>/现货分钟K线_<yyyymmdd>.csv"`, `"timezone": "UTC"`} {
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

func TestWritePlainCSVWithAChinesePrefix(t *testing.T) {
	files, store, task, p := setup(t)
	files.Format = FormatCSV
	info, err := files.Write(context.Background(), store, task, "币安现货1分钟K线", p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != "crypto/2026/币安现货1分钟K线_20261015.csv" || !strings.HasPrefix(info.Preview, "data_time,subject_id,series_tag,open\n") {
		t.Fatalf("info = %+v", info)
	}
	raw, err := os.ReadFile(files.Abs(info.Path))
	if err != nil || !strings.HasPrefix(string(raw), "data_time,subject_id,series_tag,open\n") {
		t.Fatalf("文件应是未压缩的 CSV: %v %q", err, raw[:20])
	}
}

package staging

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 纳秒精度：同一标的、同一序列相差不到 1 毫秒的两行不能互相覆盖，导出与读回保持纳秒。
func TestStageKeepsNanosecondKeys(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	at := day0.Add(time.Hour)
	res, err := s.Stage(ctx, task, []Row{
		row("BTC", at.Add(100), "", 1.0, 1.0, nil, nil),
		row("BTC", at.Add(200), "", 2.0, 2.0, nil, nil),
	}, utcDay, now0)
	if err != nil || res.Added != 2 {
		t.Fatalf("res = %+v err=%v", res, err)
	}
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	stats, _ := s.Stats(ctx, task, p)
	if !stats.Min.Equal(at.Add(100)) || !stats.Max.Equal(at.Add(200)) {
		t.Fatalf("stats = %+v", stats)
	}
	file := filepath.Join(t.TempDir(), "p.csv")
	if err := s.ExportCSV(ctx, task, p, file, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	if !strings.Contains(string(raw), "2026-10-15T01:00:00.0000001Z,BTC") || !strings.Contains(string(raw), "2026-10-15T01:00:00.0000002Z,BTC") {
		t.Fatalf("csv =\n%s", raw)
	}
}

// 全部列类型经导出、定稿、读回后与原值逐字段一致：再次写入同样的行没有变化。
func TestAllColumnTypesRoundTripThroughTheArchiveFile(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	task := Task{SpaceID: "crypto", DatasetID: "all", Columns: []Column{
		{"d", TypeDouble}, {"i", TypeInt}, {"s", TypeString}, {"b", TypeBool}, {"ts", TypeTime}, {"j", TypeJSON}, {"raw", TypeBytes},
	}}
	if err := s.EnsureTable(ctx, task); err != nil {
		t.Fatal(err)
	}
	at := day0.Add(2 * time.Hour)
	rows := []Row{
		row("BTC", at, "", 0.1, int64(math.MaxInt64), " padded ", true, at.Add(123456789), `{"a":[1,2]}`, []byte{0, 1, 0xff}),
		row("BTC", at.Add(time.Minute), "t\n2", math.Inf(-1), int64(-7), "", false, day0, `null`, []byte{}),
		row("ETH", at, "", math.NaN(), nil, nil, nil, nil, nil, nil),
		row("SOL", at, "", 1e21, int64(0), "line1\r\nline2", true, at, "", []byte("x")),
	}
	if _, err := s.Stage(ctx, task, rows, utcDay, now0); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.Partition(ctx, "crypto", "all", "2026-10-15")
	file := filepath.Join(t.TempDir(), "all.csv.gz")
	if err := s.Export(ctx, task, p, file); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkExported(ctx, "crypto", "all", p.Key, now0.Add(time.Hour), fileSHA(t, file)); err != nil {
		t.Fatal(err)
	}
	if err := s.Finalize(ctx, task, []string{p.Key}, now0, now0, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ReopenFinal(ctx, task, p.Key, file); err != nil {
		t.Fatal(err)
	}
	res, err := s.Stage(ctx, task, rows, utcDay, now0.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// NaN 在 SQLite 里存为 NULL，因此 ETH 的 d 列读回为 NULL，与写入的 NaN 比较算一次变化；其余行必须完全一致。
	if res.Added != 0 || res.Changed > 1 {
		t.Fatalf("读回后重复写入应没有变化: %+v", res)
	}
	got := readGzip(t, file)
	for _, want := range []string{
		`data_time,subject_id,series_tag,d,i,s,b,ts,j,raw`,
		`BTC,"",0.1,9223372036854775807," padded ",true,2026-10-15T02:00:00.123456789Z,"{""a"":[1,2]}",AAH/`,
		`BTC,"t` + "\n" + `2",-Inf,-7,"",false,2026-10-15T00:00:00Z,null,""`,
		`SOL,"",1e+21,0,"line1` + "\r\n" + `line2",true,2026-10-15T02:00:00Z,"",eA==`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("csv 缺少 %q:\n%s", want, got)
		}
	}
}

// 损坏的文件读回时报错，不把半个分区写进暂存表。
func TestReopenFinalRejectsMalformedFiles(t *testing.T) {
	s, ctx, task := openTest(t), context.Background(), klineTask()
	_ = s.EnsureTable(ctx, task)
	_, _ = s.Stage(ctx, task, []Row{row("BTC", day0, "", 1.0, 1.0, nil, nil)}, utcDay, now0)
	p, _, _ := s.Partition(ctx, "crypto", "ds_1m", "2026-10-15")
	file := filepath.Join(t.TempDir(), "p.csv")
	_ = s.ExportCSV(ctx, task, p, file, false)
	_ = s.MarkExported(ctx, "crypto", "ds_1m", p.Key, now0.Add(time.Hour), "sha")
	_ = s.Finalize(ctx, task, []string{p.Key}, now0, now0, true)
	for name, body := range map[string]string{
		"field count": "data_time,subject_id,series_tag,open,close,note,n\n2026-10-15T00:00:00Z,BTC,,1.0\n",
		"bad number":  "data_time,subject_id,series_tag,open,close,note,n\n2026-10-15T00:00:00Z,BTC,\"\",abc,1.0,,\n",
		"open quote":  "data_time,subject_id,series_tag,open,close,note,n\n2026-10-15T00:00:00Z,BTC,\"x,1.0,1.0,,\n",
	} {
		bad := filepath.Join(t.TempDir(), "bad.csv")
		if err := os.WriteFile(bad, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.ReopenFinal(ctx, task, p.Key, bad); err == nil {
			t.Fatalf("%s: 应报错", name)
		}
		if stats, _ := s.Stats(ctx, task, p); stats.Rows != 0 {
			t.Fatalf("%s: 失败的读回不能留下行: %+v", name, stats)
		}
	}
}

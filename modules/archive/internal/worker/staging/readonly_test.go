package staging

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func strp(s string) *string { return &s }

func cellText(c *string) string {
	if c == nil {
		return "<null>"
	}
	return *c
}

// 最新数据按 data_time 倒序、before 翻页、limit 截断；单元格按 CSV 规则格式化，缺失值与空串可区分；dataset.json 有而暂存表没有的列为缺失值。
func TestRecentRowsOrderPagingAndFormatting(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	task := Task{SpaceID: "crypto", DatasetID: "all", Columns: []Column{{"d", TypeDouble}, {"b", TypeBool}, {"ts", TypeTime}, {"s", TypeString}, {"i", TypeInt}}}
	if err := s.EnsureTable(ctx, task); err != nil {
		t.Fatal(err)
	}
	at := day0.Add(2 * time.Hour)
	rows := []Row{
		row("BTC", at, "", 1.0, true, at.Add(5), "", int64(math.MaxInt64)),
		row("BTC", at.Add(time.Minute), "", math.Inf(1), false, nil, nil, int64(-1)),
		row("BTC", at.Add(2*time.Minute), "", 0.5, nil, nil, "x", nil),
		row("ETH", at.Add(3*time.Minute), "", 9.0, true, nil, "y", nil),
	}
	if _, err := s.Stage(ctx, task, rows, utcDay, now0); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	// dataset.json 比暂存表多一列 extra。
	view := task
	view.Columns = append(append([]Column{}, task.Columns...), Column{"extra", TypeString})
	got, err := ro.Recent(ctx, view, "BTC", time.Time{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Columns) != 9 || got.Columns[0].Name != "data_time" || got.Columns[8].Name != "extra" {
		t.Fatalf("columns = %+v", got.Columns)
	}
	if len(got.Rows) != 2 || cellText(got.Rows[0][0]) != "2026-10-15T02:02:00Z" || cellText(got.Rows[1][0]) != "2026-10-15T02:01:00Z" {
		t.Fatalf("rows = %v", got.Rows)
	}
	want := []string{"2026-10-15T02:01:00Z", "BTC", "", "+Inf", "false", "<null>", "<null>", "-1", "<null>"}
	for i, w := range want {
		if cellText(got.Rows[1][i]) != w {
			t.Fatalf("cell %d = %q, want %q", i, cellText(got.Rows[1][i]), w)
		}
	}
	page, err := ro.Recent(ctx, view, "BTC", at.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("before page = %v", page.Rows)
	}
	first := page.Rows[0]
	for i, w := range []*string{strp("2026-10-15T02:00:00Z"), strp("BTC"), strp(""), strp("1.0"), strp("true"), strp("2026-10-15T02:00:00.000000005Z"), strp(""), strp("9223372036854775807"), nil} {
		if (w == nil) != (first[i] == nil) || w != nil && *w != *first[i] {
			t.Fatalf("cell %d = %q", i, cellText(first[i]))
		}
	}
	none, err := ro.Recent(ctx, Task{SpaceID: "crypto", DatasetID: "missing"}, "BTC", time.Time{}, 10)
	if err != nil || len(none.Rows) != 0 {
		t.Fatalf("missing table = %+v, %v", none, err)
	}
}

// 只读连接不能写，也不会创建库文件；关闭后不挡写连接的 wal_checkpoint(TRUNCATE)。
func TestReadOnlyConnectionCannotWriteAndReleasesTheWAL(t *testing.T) {
	s, ctx := openTest(t), context.Background()
	task := klineTask()
	if err := s.EnsureTable(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stage(ctx, task, []Row{row("BTC", day0, "", 1.0, 2.0, "n", int64(1))}, utcDay, now0); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(filepath.Join(t.TempDir(), "absent.db")); err == nil {
		t.Fatal("只读打开不存在的库应报错")
	}
	ro, err := OpenReadOnly(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.SetResumeFrom(ctx, "crypto", "ds_1m", now0); err == nil {
		t.Fatal("只读连接写入成功")
	}
	if _, err := ro.Recent(ctx, task, "BTC", time.Time{}, 10); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ro.Partition(ctx, "crypto", "ds_1m", "2026-10-15"); err != nil || !ok {
		t.Fatalf("partition = %v %v", ok, err)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		t.Fatal(err)
	}
	if busy != 0 {
		t.Fatalf("checkpoint busy = %d", busy)
	}
}

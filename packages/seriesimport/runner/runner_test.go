package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/seriesformat"
	"github.com/mooyang-code/moox/packages/seriesimport/checkpoint"
	"github.com/stretchr/testify/require"
)

var fields = []seriesformat.Field{{Name: "close", Type: seriesformat.TypeDouble}, {Name: "volume", Type: seriesformat.TypeDouble}}

type recorder struct {
	name   string
	mu     sync.Mutex
	chunks []Chunk
	failAt int // 第几次写入失败（从 1 开始），0 表示不失败
	calls  int
}

func (r *recorder) Name() string { return r.name }

func (r *recorder) Write(_ context.Context, c Chunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.failAt > 0 && r.calls == r.failAt {
		return errors.New("boom")
	}
	r.chunks = append(r.chunks, c)
	return nil
}

func writeCSV(t *testing.T, dir, name, header string, rows []string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(header+"\n"+strings.Join(rows, "\n")+"\n"), 0o600))
	return path
}

func minuteRows(from, to int) []string {
	var rows []string
	for m := from; m < to; m++ {
		rows = append(rows, fmt.Sprintf("2026-10-01 %02d:%02d:00,%d,%d", m/60, m%60, 100+m, m))
	}
	return rows
}

func baseRequest(paths ...string) Request {
	return Request{Space: "crypto", Dataset: "spot", Freq: "1m", Paths: paths, Format: "csv", Step: time.Minute, BatchRows: 4}
}

func TestRunBatchesFilesAndWritesEveryTarget(t *testing.T) {
	dir := t.TempDir()
	file := writeCSV(t, dir, "BTCUSDT.csv", "data_time,close,volume", minuteRows(0, 10))
	a, b := &recorder{name: "storage"}, &recorder{name: "factor"}
	sum, err := Run(context.Background(), baseRequest(file), Meta{Fields: fields}, []Target{a, b}, nil)
	require.NoError(t, err)
	require.Equal(t, "imported", sum.Status)
	require.Equal(t, 10, sum.Rows)
	require.Equal(t, "BTCUSDT", sum.Files[0].Subject, "文件名是标的 ID")
	for _, tgt := range []*recorder{a, b} {
		require.Len(t, tgt.chunks, 3, "10 行按每批 4 行分成 4+4+2")
		first, last := tgt.chunks[0], tgt.chunks[2]
		require.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), first.From)
		require.Equal(t, time.Date(2026, 10, 1, 0, 4, 0, 0, time.UTC), first.To, "批的区间是 [首行, 末行 + 一个周期)")
		require.Equal(t, time.Date(2026, 10, 1, 0, 10, 0, 0, time.UTC), last.To)
		require.Equal(t, "crypto", first.Space)
		require.False(t, first.AllowMissing)
	}
	require.Equal(t, map[string]int64{"storage": 3, "factor": 3}, sum.Files[0].Batches)
}

// 同一个时间点的多个序列不会被拆到两批里：整段替换会删除区间内所有序列，拆开会让后一批删掉前一批写的行。
func TestRebatchingNeverSplitsATimestamp(t *testing.T) {
	dir := t.TempDir()
	var rows []string
	for m := 0; m < 6; m++ {
		for _, tag := range []string{"a", "b", "c"} {
			rows = append(rows, fmt.Sprintf("2026-10-01 00:%02d:00,%s,%d,1", m, tag, 100+m))
		}
	}
	file := writeCSV(t, dir, "ETH.csv", "data_time,series_tag,close,volume", rows)
	rec := &recorder{name: "factor"}
	req := baseRequest(file)
	req.BatchRows = 4
	_, err := Run(context.Background(), req, Meta{Fields: fields}, []Target{rec}, nil)
	require.NoError(t, err)
	total := 0
	for _, c := range rec.chunks {
		total += c.Batch.Len()
		// 批内每个时间点都有全部三个序列。
		counts := map[time.Time]int{}
		for _, at := range c.Batch.Times {
			counts[at]++
		}
		for at, n := range counts {
			require.Equal(t, 3, n, "时间点 %s 被拆开了", at)
		}
	}
	require.Equal(t, 18, total)
	require.Greater(t, len(rec.chunks), 1)
}

func TestRunValidatesEverythingBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	good := writeCSV(t, dir, "A.csv", "data_time,close,volume", minuteRows(0, 5))
	bad := writeCSV(t, dir, "B.csv", "data_time,close,volume", append(minuteRows(0, 2), minuteRows(4, 6)...)) // 缺 t2 t3
	rec := &recorder{name: "factor"}

	_, err := Run(context.Background(), baseRequest(good, bad), Meta{Fields: fields}, []Target{rec}, nil)
	require.ErrorContains(t, err, "缺 2 个周期")
	require.ErrorContains(t, err, "--allow-missing")
	require.Empty(t, rec.chunks, "有文件不合法时，不写任何内容")

	// 允许缺期：校验通过，目标收到 AllowMissing，报告里列出缺的周期。
	req := baseRequest(good, bad)
	req.AllowMissing = true
	sum, err := Run(context.Background(), req, Meta{Fields: fields}, []Target{rec}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, sum.Files[1].MissingPeriods)
	require.Equal(t, []string{"2026-10-01T00:02:00Z", "2026-10-01T00:03:00Z"}, sum.Files[1].Missing)
	for _, c := range rec.chunks {
		require.True(t, c.AllowMissing)
	}
}

func TestRunRejectsStructuralProblems(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	run := func(file string, meta Meta) error {
		_, err := Run(ctx, baseRequest(file), meta, []Target{&recorder{name: "t"}}, nil)
		return err
	}
	require.ErrorContains(t, run(writeCSV(t, dir, "a.csv", "data_time,close", minuteRows(0, 2)[:0]), Meta{Fields: fields}), "缺少数据集字段")
	require.ErrorContains(t, run(writeCSV(t, dir, "b.csv", "data_time,close,volume", nil), Meta{Fields: fields}), "没有数据行")
	require.ErrorContains(t, run(writeCSV(t, dir, "c.csv", "data_time,close,volume", []string{"2026-10-01 00:00:30,1,1"}), Meta{Fields: fields}), "没有按")
	require.ErrorContains(t, run(writeCSV(t, dir, "d.csv", "data_time,close,volume", []string{"2026-10-01 00:01:00,1,1", "2026-10-01 00:00:00,1,1"}), Meta{Fields: fields}), "升序")
	require.ErrorContains(t, run(filepath.Join(dir, "missing.csv"), Meta{Fields: fields}), "no such file")

	// 标的不在数据集的标签范围内：拒绝，不自动绑定。
	scoped := Meta{Fields: fields, SubjectInScope: func(context.Context, string) (bool, error) { return false, nil }}
	require.ErrorContains(t, run(writeCSV(t, dir, "e.csv", "data_time,close,volume", minuteRows(0, 2)), scoped), "不在数据集的标签范围内")

	_, err := Run(ctx, Request{}, Meta{Fields: fields}, nil, nil)
	require.Error(t, err)
	_, err = Run(ctx, baseRequest(writeCSV(t, dir, "f.csv", "data_time,close,volume", minuteRows(0, 2))), Meta{}, nil, nil)
	require.ErrorContains(t, err, "没有可导入的字段")
	_, err = Run(ctx, baseRequest(writeCSV(t, dir, "g.csv", "data_time,close,volume", minuteRows(0, 2))), Meta{Fields: fields}, nil, nil)
	require.ErrorContains(t, err, "没有写入目标")
	req := baseRequest(writeCSV(t, dir, "h.csv", "data_time,close,volume", minuteRows(0, 2)))
	req.Format = "parquet"
	_, err = Run(ctx, req, Meta{Fields: fields}, []Target{&recorder{name: "t"}}, nil)
	require.ErrorContains(t, err, "不支持的数据格式")
}

func TestDryRunValidatesWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	file := writeCSV(t, dir, "BTC.csv", "data_time,close,volume", minuteRows(0, 5))
	rec := &recorder{name: "factor"}
	req := baseRequest(file)
	req.DryRun = true
	sum, err := Run(context.Background(), req, Meta{Fields: fields}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "dry_run", sum.Status)
	require.Equal(t, 5, sum.Rows)
	require.Empty(t, rec.chunks)
}

func TestDirectoryExpansionAndSubjectOverride(t *testing.T) {
	dir := t.TempDir()
	writeCSV(t, dir, "BTC.csv", "data_time,close,volume", minuteRows(0, 3))
	writeCSV(t, dir, "ETH.csv", "data_time,close,volume", minuteRows(0, 3))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600))
	rec := &recorder{name: "factor"}
	sum, err := Run(context.Background(), baseRequest(dir), Meta{Fields: fields}, []Target{rec}, nil)
	require.NoError(t, err)
	require.Len(t, sum.Files, 2)
	require.Equal(t, []string{"BTC", "ETH"}, []string{sum.Files[0].Subject, sum.Files[1].Subject})

	one := writeCSV(t, t.TempDir(), "whatever.csv", "data_time,close,volume", minuteRows(0, 3))
	req := baseRequest(one)
	req.Subject = "SOL"
	rec = &recorder{name: "factor"}
	_, err = Run(context.Background(), req, Meta{Fields: fields}, []Target{rec}, nil)
	require.NoError(t, err)
	require.Equal(t, "SOL", rec.chunks[0].Subject)

	_, err = Run(context.Background(), baseRequest(t.TempDir()), Meta{Fields: fields}, []Target{rec}, nil)
	require.ErrorContains(t, err, "没有找到可导入的文件")
}

// 写入中途失败：已写完的批有断点，重跑同一请求从下一批继续；整个文件写完后再重跑直接跳过。
func TestResumeFromCheckpointAndRecalcFlag(t *testing.T) {
	dir := t.TempDir()
	file := writeCSV(t, dir, "BTC.csv", "data_time,close,volume", minuteRows(0, 12))
	cps, err := checkpoint.Open(filepath.Join(t.TempDir(), "importer.db"))
	require.NoError(t, err)
	defer cps.Close()
	req := baseRequest(file)
	req.Recalc = true

	flaky := &recorder{name: "factor", failAt: 3} // 12 行分 3 批，第 3 批失败
	sum, err := Run(context.Background(), req, Meta{Fields: fields}, []Target{flaky}, cps)
	require.Error(t, err)
	require.Equal(t, "failed", sum.Status)
	require.Len(t, flaky.chunks, 2)
	for _, c := range flaky.chunks {
		require.True(t, c.RecordPending, "--recalc 要求目标记失效重算")
	}
	entries, err := cps.List(sum.JobID)
	require.NoError(t, err)
	require.Equal(t, "failed", entries[0].Status)
	require.Equal(t, "boom", entries[0].Error)

	// 重跑：前两批跳过，只写第 3 批。
	healthy := &recorder{name: "factor"}
	sum, err = Run(context.Background(), req, Meta{Fields: fields}, []Target{healthy}, cps)
	require.NoError(t, err)
	require.Len(t, healthy.chunks, 1)
	require.Equal(t, int64(2), sum.Files[0].Skipped["factor"])
	require.Equal(t, time.Date(2026, 10, 1, 0, 8, 0, 0, time.UTC), healthy.chunks[0].From)

	// 全部完成之后再跑：整文件跳过。
	again := &recorder{name: "factor"}
	sum, err = Run(context.Background(), req, Meta{Fields: fields}, []Target{again}, cps)
	require.NoError(t, err)
	require.Empty(t, again.chunks)
	require.Equal(t, int64(3), sum.Files[0].Skipped["factor"])

	// 文件内容变了（大小不同）：任务 ID 变化，重新导入。
	require.NoError(t, os.WriteFile(file, []byte("data_time,close,volume\n"+strings.Join(minuteRows(0, 13), "\n")+"\n"), 0o600))
	changed := &recorder{name: "factor"}
	_, err = Run(context.Background(), req, Meta{Fields: fields}, []Target{changed}, cps)
	require.NoError(t, err)
	require.NotEmpty(t, changed.chunks)
}

type finishing struct {
	recorder
	finished int
	err      error
}

func (f *finishing) Finish(context.Context) error { f.finished++; return f.err }

func TestFinisherRunsOnceAfterAllFiles(t *testing.T) {
	dir := t.TempDir()
	writeCSV(t, dir, "A.csv", "data_time,close,volume", minuteRows(0, 3))
	writeCSV(t, dir, "B.csv", "data_time,close,volume", minuteRows(0, 3))
	tgt := &finishing{recorder: recorder{name: "storage"}}
	_, err := Run(context.Background(), baseRequest(dir), Meta{Fields: fields}, []Target{tgt}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, tgt.finished)
	require.Len(t, tgt.chunks, 2)

	tgt = &finishing{recorder: recorder{name: "storage"}, err: errors.New("view 没有追平")}
	sum, err := Run(context.Background(), baseRequest(dir), Meta{Fields: fields}, []Target{tgt}, nil)
	require.ErrorContains(t, err, "view 没有追平")
	require.Equal(t, "failed", sum.Status)
}

// 补更早的历史（文件完全早于本地已有数据）：批从后往前写，每一批都与已有数据或刚写的那一批相接；
// 断点按这个顺序计数，中断后续跑不会漏批。
func TestRunWritesBatchesBackwardsWhenPrependingHistory(t *testing.T) {
	dir := t.TempDir()
	path := writeCSV(t, dir, "BTC.csv", "data_time,close,volume", minuteRows(0, 10))
	local := time.Date(2026, 10, 1, 0, 10, 0, 0, time.UTC)
	meta := Meta{Fields: fields, LocalBounds: func(context.Context, string) (time.Time, time.Time, bool, error) {
		return local, local.Add(5 * time.Minute), true, nil
	}}
	rec := &recorder{name: "factor"}
	_, err := Run(context.Background(), baseRequest(path), meta, []Target{rec}, nil)
	require.NoError(t, err)
	require.Len(t, rec.chunks, 3)
	require.Equal(t, local.Add(-2*time.Minute), rec.chunks[0].From, "先写最晚的一批：[8, 10)，与本地起点相接")
	require.Equal(t, local, rec.chunks[0].To)
	require.Equal(t, local.Add(-6*time.Minute), rec.chunks[1].From)
	require.Equal(t, local.Add(-10*time.Minute), rec.chunks[2].From)

	// 文件不完全早于本地数据（有重叠）：保持从前往后。
	rec2 := &recorder{name: "factor"}
	overlap := Meta{Fields: fields, LocalBounds: func(context.Context, string) (time.Time, time.Time, bool, error) {
		return local.Add(-3 * time.Minute), local.Add(5 * time.Minute), true, nil
	}}
	_, err = Run(context.Background(), baseRequest(path), overlap, []Target{rec2}, nil)
	require.NoError(t, err)
	require.Equal(t, local.Add(-10*time.Minute), rec2.chunks[0].From)
}

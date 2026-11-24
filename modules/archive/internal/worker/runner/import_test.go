package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// upsert 造一行导入请求：open、close 两列。
func upsert(subject string, at time.Time, open, close float64) *storagepb.RowFieldUpsert {
	return &storagepb.RowFieldUpsert{
		Key: &storagepb.RowKey{SpaceId: "crypto", DatasetId: "ds_1m", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
			SubjectId: subject, Freq: "1m", DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: "spot",
		}}},
		Fields: []*storagepb.FieldValue{
			{FieldId: "open", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: open}}},
			{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: close}}},
		},
	}
}

func batchOf(rows ...*storagepb.RowFieldUpsert) *storagepb.PrimaryUpsertFieldsReq {
	return &storagepb.PrimaryUpsertFieldsReq{SpaceId: "crypto", DatasetId: "ds_1m", Rows: rows}
}

// importRun 起一条导入运行，返回发批次、发结束与等结果的函数。
type importRun struct {
	batches  chan ImportBatch
	finished chan struct{}
	done     chan *Outcome
	cancel   context.CancelFunc
}

func startImport(t *testing.T, e *env) *importRun {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	run := &importRun{batches: make(chan ImportBatch), finished: make(chan struct{}), done: make(chan *Outcome, 1), cancel: cancel}
	t.Cleanup(cancel)
	assignment := e.assignment(func(a *archivepb.RunAssignment) { a.Kind = "import" })
	r := e.runner(2000)
	go func() {
		out, err := r.Execute(ctx, assignment, nil, &ImportFeed{Batches: run.batches, Finished: run.finished})
		if err != nil {
			out = &Outcome{Status: "error", Message: err.Error()}
		}
		run.done <- out
	}()
	return run
}

func (r *importRun) write(t *testing.T, req *storagepb.PrimaryUpsertFieldsReq) ImportResult {
	t.Helper()
	result := make(chan ImportResult, 1)
	r.batches <- ImportBatch{Req: req, Result: result}
	return <-result
}

func (r *importRun) wait(t *testing.T) *Outcome {
	t.Helper()
	select {
	case out := <-r.done:
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("导入运行没有结束")
		return nil
	}
}

func TestImportMergesIntoAFinalPartitionAndExportsOnFinish(t *testing.T) {
	e := newEnv(t)
	finalDay(t, e) // 10-10 已定稿，30 行 BTC 01:00–01:29（series_tag 为空）
	e.now = e.now.Add(time.Hour)
	run := startImport(t, e)
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	got := run.write(t, batchOf(upsert("ETH", day.Add(2*time.Hour), 9, 9.5), upsert("ETH", day.Add(3*time.Hour), 8, 8.5)))
	if got.Err != nil || got.Added != 2 {
		t.Fatalf("got = %+v", got)
	}
	// 同键再写一次：一行相同、一行覆盖。
	got = run.write(t, batchOf(upsert("ETH", day.Add(2*time.Hour), 9, 9.5), upsert("ETH", day.Add(3*time.Hour), 7, 7.5)))
	if got.Err != nil || got.Same != 1 || got.Changed != 1 {
		t.Fatalf("got = %+v", got)
	}
	// 不合法的批次整批拒绝，运行继续。
	bad := upsert("ETH", day.Add(4*time.Hour), 1, 1)
	bad.Fields = append(bad.Fields, &storagepb.FieldValue{FieldId: "volume", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 1}}})
	for _, req := range []*storagepb.PrimaryUpsertFieldsReq{
		batchOf(bad),
		{SpaceId: "crypto", DatasetId: "other", Rows: []*storagepb.RowFieldUpsert{upsert("ETH", day, 1, 1)}},
		batchOf(&storagepb.RowFieldUpsert{Key: &storagepb.RowKey{SpaceId: "crypto", DatasetId: "ds_1m"}}),
	} {
		if got := run.write(t, req); !errors.Is(got.Err, ErrInvalidBatch) {
			t.Fatalf("应整批拒绝: %+v", got)
		}
	}
	close(run.finished)
	out := run.wait(t)
	p := report(out, "2026-10-10")
	if out.Status != "succeeded" || p.GetRowCount() != 32 || p.GetStatus() != "final" || p.GetExportCount() != 2 || !strings.Contains(out.Message, "导入 4 行") {
		t.Fatalf("out=%+v p=%v", out, p)
	}
	if csv := readFile(t, e.root, p.GetFilePath()); !strings.Contains(csv, "2026-10-10T03:00:00Z,ETH,spot,7.0,7.5") || strings.Count(csv, "\n") != 33 {
		t.Fatalf("导出的文件 = %q", csv)
	}
}

func TestImportIdleTimeoutExportsWrittenRowsAndFails(t *testing.T) {
	old := importIdleTimeout
	importIdleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { importIdleTimeout = old })
	e := newEnv(t)
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	run := startImport(t, e)
	if got := run.write(t, batchOf(upsert("BTC", time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC), 1, 1))); got.Err != nil {
		t.Fatal(got.Err)
	}
	out := run.wait(t)
	p := report(out, "2026-10-01")
	if out.Status != "failed" || !strings.Contains(out.Message, "已写入的行已照常导出") || p.GetStatus() != "final" || p.GetFilePath() == "" {
		t.Fatalf("out=%+v p=%v", out, p)
	}
}

func TestImportCancelExportsWrittenRows(t *testing.T) {
	e := newEnv(t)
	e.now = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	run := startImport(t, e)
	if got := run.write(t, batchOf(upsert("BTC", time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC), 1, 1))); got.Err != nil {
		t.Fatal(got.Err)
	}
	run.cancel()
	out := run.wait(t)
	if out.Status != "canceled" || !strings.Contains(out.Message, "重跑") || report(out, "2026-10-01").GetFilePath() == "" {
		t.Fatalf("out = %+v", out)
	}
}

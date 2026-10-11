package klinecheck

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

type fakeFunctions struct{}

func (fakeFunctions) EnsureBackfillFunction(context.Context, string) (string, error) {
	return "moox-fetch-plan-backfill-crypto-spot-nanjing", nil
}

// healingInvoker 记录调用，并在成功时把请求覆盖的缺失 K 线补上。
type healingInvoker struct {
	mu     sync.Mutex
	rows   *fakeRows
	events []map[string]any
	err    error
	fail   bool
}

func (h *healingInvoker) Invoke(_ context.Context, _, _ string, event map[string]any, _ cloudnodepb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event)
	if h.err != nil {
		return scfinvoker.InvocationResult{}, h.err
	}
	if h.fail {
		return scfinvoker.InvocationResult{Result: map[string]any{"success": false, "message": "provider error"}}, nil
	}
	data := event["data"].(map[string]any)
	item := data["items"].([]any)[0].(map[string]any)
	from, _ := time.Parse(time.RFC3339, item["start_time"].(string))
	to, _ := time.Parse(time.RFC3339, item["end_time"].(string))
	subject := item["subject_id"].(string)
	for t := from; t.Before(to); t = t.Add(time.Minute) {
		delete(h.rows.missing[subject], t.Unix())
	}
	return scfinvoker.InvocationResult{Result: map[string]any{"success": true}}, nil
}

func newBackfillFixture(t *testing.T) (*Backfiller, *healingInvoker, *fakeRows, time.Time) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })

	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	options := marketfetch.PlanOptions{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-nanjing"}}, DefaultLatenessS: 15, Fetch: marketfetch.PlanFetch{Concurrency: 5}}
	desired, err := marketfetch.BuildDesiredState(options, []marketfetch.TaskGroup{{
		Provider: "binance", SourceID: "binance", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds", Frequency: "1m",
		Subjects: []string{"BTC-USDT", "ETH-USDT"},
	}}, nil)
	require.NoError(t, err)
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string, int64, bool) (int64, error) { return base.Unix(), nil })
	require.NoError(t, err)
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	record, err := db.FetchPlans().Create(context.Background(), "crypto", string(raw), "{}")
	require.NoError(t, err)
	require.NoError(t, db.FetchPlans().Update(context.Background(), record.ID, store.FetchPlanApplied, "{}", ""))

	rows := &fakeRows{missing: map[string]map[int64]bool{"BTC-USDT": {}, "ETH-USDT": {}}}
	for _, minute := range []int{3, 4, 5} {
		rows.missing["BTC-USDT"][base.Add(time.Duration(minute)*time.Minute).Unix()] = true
	}
	rows.missing["ETH-USDT"][base.Add(10*time.Minute).Unix()] = true
	now := base.Add(48 * time.Hour)
	checker := &Checker{Plans: db.FetchPlans(), Rows: rows, Now: func() time.Time { return now }}
	invoker := &healingInvoker{rows: rows}
	backfiller := &Backfiller{
		Checker: checker, Store: db.Backfill(), Plans: db.FetchPlans(), Invoker: invoker, BaseCtx: context.Background(), Now: func() time.Time { return now },
		Functions: func(string) FunctionProvider { return fakeFunctions{} },
	}
	return backfiller, invoker, rows, base
}

func TestBackfillRunsChunksAndVerifiesGaps(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	ctx := context.Background()
	jobID, chunks, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.Equal(t, 2, chunks)

	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
	require.Equal(t, 2, status.Counts[store.BackfillSucceeded])

	require.Len(t, invoker.events, 2)
	for _, event := range invoker.events {
		data := event["data"].(map[string]any)
		require.Equal(t, "backfill", data["batch_kind"])
		require.Equal(t, true, data["write_id_by_content"])
		require.NotContains(t, data, "require_period_commit")
		item := data["items"].([]any)[0].(map[string]any)
		require.Contains(t, []any{"BTCUSDT", "ETHUSDT"}, item["symbol"])
	}
	// 已补齐：再提交不会产生作业。
	again, count, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.Empty(t, again)
	require.Zero(t, count)
}

func TestBackfillForceSplitsLongRangesAndRespectsInFlightLimit(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	backfiller.MaxInFlight = 2
	ctx := context.Background()
	jobID, chunks, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: base, To: base.Add(2500 * time.Minute), Force: true}, "manual")
	require.NoError(t, err)
	require.Equal(t, 3, chunks, "1000 + 1000 + 500 bars")

	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.Len(t, invoker.events, 2, "at most two chunks are claimed per tick")
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	status, _ := backfiller.Status(ctx, jobID)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
}

func TestBackfillUnknownOutcomeKeepsSlotAndFailureIsReported(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	ctx := context.Background()
	jobID, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	invoker.err = context.DeadlineExceeded
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	status, _ := backfiller.Status(ctx, jobID)
	require.Equal(t, 2, status.Counts[store.BackfillRunning], "an unknown outcome is not a failure")
	require.Empty(t, status.Failed)

	invoker.err, invoker.fail = nil, true
	// 名额未到最迟结束时间不会被重新认领。
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.Len(t, invoker.events, 2)
}

func TestBackfillFailureMarksJobFailed(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	invoker.fail = true
	ctx := context.Background()
	jobID, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	status, _ := backfiller.Status(ctx, jobID)
	require.Equal(t, store.BackfillFailed, status.Job.Status)
	require.Len(t, status.Failed, 2)
	require.Contains(t, status.Failed[0].LastError, "provider error")
}

func TestBackfillCancelStopsPendingChunks(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	ctx := context.Background()
	jobID, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.NoError(t, backfiller.Cancel(ctx, jobID))
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.Empty(t, invoker.events)
	status, _ := backfiller.Status(ctx, jobID)
	require.Equal(t, store.BackfillCanceled, status.Job.Status)
}

func TestAutoScanCreatesOneJobPerHourAndAdvancesWatermark(t *testing.T) {
	backfiller, _, _, base := newBackfillFixture(t)
	ctx := context.Background()
	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	jobs, err := backfiller.Store.ActiveJobs(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "auto", jobs[0].Source)
	mark, err := backfiller.Store.Watermark(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.True(t, mark.After(base), "the watermark moves forward")

	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	jobs, _ = backfiller.Store.ActiveJobs(ctx, "crypto")
	require.Len(t, jobs, 1, "scans are spaced an hour apart and never stack on an unfinished auto job")
}

package klinecheck

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	cloudfunctionpb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
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
	// noWrite 让函数“成功”却不写入任何行，模拟行情源本身没有这根 K 线。
	noWrite bool
	// sourceEmpty 让函数按真实路径失败：行情源返回空结果（no closed bar），不写入任何行。
	sourceEmpty bool
	// healOne 让函数每次只补上请求范围里最早缺的一根，模拟每次缺的都不一样的暂时缺失。
	healOne bool
}

func (h *healingInvoker) Invoke(_ context.Context, _, _ string, event map[string]any, _ cloudfunctionpb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event)
	if h.err != nil {
		return scfinvoker.InvocationResult{}, h.err
	}
	if h.fail {
		return scfinvoker.InvocationResult{Result: map[string]any{"success": false, "message": "provider error"}}, nil
	}
	if h.sourceEmpty {
		return scfinvoker.InvocationResult{Result: map[string]any{"success": false, "message": "item failed: no closed bar"}}, nil
	}
	if h.noWrite {
		return scfinvoker.InvocationResult{Result: map[string]any{"success": true}}, nil
	}
	data := event["data"].(map[string]any)
	item := data["items"].([]any)[0].(map[string]any)
	from, _ := time.Parse(time.RFC3339, item["start_time"].(string))
	to, _ := time.Parse(time.RFC3339, item["end_time"].(string))
	subject := item["subject_id"].(string)
	h.rows.mu.Lock()
	for t := from; t.Before(to); t = t.Add(time.Minute) {
		if h.healOne && !h.rows.missing[subject][t.Unix()] {
			continue
		}
		delete(h.rows.missing[subject], t.Unix())
		if h.healOne {
			break
		}
	}
	h.rows.mu.Unlock()
	return scfinvoker.InvocationResult{Result: map[string]any{"success": true}}, nil
}

func newBackfillFixture(t *testing.T) (*Backfiller, *healingInvoker, *fakeRows, time.Time) {
	t.Helper()
	return newBackfillFixtureWithModes(t, nil)
}

func newBackfillFixtureWithModes(t *testing.T, modes map[string]marketfetch.DatasetMode) (*Backfiller, *healingInvoker, *fakeRows, time.Time) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })

	// 分块名额的有效期由存储层按真实时钟判断，夹具的“当前时间”不能早于真实时间，否则运行中的名额立即过期。
	// 取下一个 5 分钟槽内、已过 2 分 30 秒偏移的时刻，计划生效点在它之前 48 小时。
	const slot = 5 * time.Minute
	now := time.Now().UTC().Add(slot).Truncate(slot).Add(3 * time.Minute)
	base := now.Add(-48*time.Hour - 3*time.Minute)
	options := marketfetch.PlanOptions{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-nanjing"}}, DefaultLatenessS: 15, Fetch: marketfetch.PlanFetch{Concurrency: 5}, Modes: modes}
	desired, err := marketfetch.BuildDesiredState(options, []marketfetch.TaskGroup{{
		Provider: "binance", SourceID: "binance", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds", Frequency: "1m",
		Subjects: []string{"BTC-USDT", "ETH-USDT"},
	}}, nil)
	require.NoError(t, err)
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return base.Unix(), nil })
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
	checker := &Checker{Plans: db.FetchPlans(), Rows: rows, Absences: db.Backfill(), Now: func() time.Time { return now }}
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
		require.Equal(t, true, data["plan_request"])
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

// 强制重拉可以补采早于采集计划生效点的缺口（数据来自旧的采集方式，计划时间线之外），分块完成后读回确认；
// 不强制时同一范围没有缺口可补。
func TestBackfillForceFillsAGapBeforeTheFirstPlan(t *testing.T) {
	backfiller, invoker, rows, base := newBackfillFixture(t)
	ctx := context.Background()
	gapAt := base.Add(-30 * time.Minute)
	rows.mu.Lock()
	rows.missing["BTC-USDT"][gapAt.Unix()] = true
	rows.mu.Unlock()

	jobID, chunks, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: gapAt, To: gapAt.Add(time.Minute)}, "manual")
	require.NoError(t, err)
	require.Empty(t, jobID)
	require.Zero(t, chunks, "不强制时生效点之前不在检查范围内")

	jobID, chunks, err = backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: gapAt, To: gapAt.Add(time.Minute), Force: true}, "manual")
	require.NoError(t, err)
	require.NotEmpty(t, jobID)
	require.Equal(t, 1, chunks)

	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
	require.Len(t, invoker.events, 1)
	rows.mu.Lock()
	defer rows.mu.Unlock()
	require.False(t, rows.missing["BTC-USDT"][gapAt.Unix()], "函数写入后复查已读到这根 K 线")
}

// 早于计划生效点的分块完成后同样读回确认：函数“成功”却没有写入时不会被当成已补齐，
// 先退回重跑，两次缺的一样才确认为行情源没有，写入根数为 0。
func TestBackfillBeforeTheFirstPlanIsVerifiedByReadingBack(t *testing.T) {
	backfiller, invoker, rows, base := newBackfillFixture(t)
	ctx := context.Background()
	gapAt := base.Add(-30 * time.Minute)
	rows.mu.Lock()
	for minute := 0; minute < 3; minute++ {
		rows.missing["BTC-USDT"][gapAt.Add(time.Duration(minute)*time.Minute).Unix()] = true
	}
	rows.mu.Unlock()
	invoker.noWrite = true

	jobID, chunks, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: gapAt, To: gapAt.Add(3 * time.Minute), Force: true}, "manual")
	require.NoError(t, err)
	require.Equal(t, 1, chunks)
	step(t, backfiller, 1)
	_, stored, err := backfiller.Store.GetJob(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillPending, stored[0].Status)
	require.Contains(t, stored[0].LastError, "3 bars are still missing")

	step(t, backfiller, 2)
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
	_, stored, _ = backfiller.Store.GetJob(ctx, jobID)
	require.Zero(t, stored[0].Rows)
	require.Equal(t, "3 bars are absent at the source", stored[0].LastError)
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

func TestAutoScanAdvancesWatermarkOnlyAfterTheJobSucceeds(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	ctx := context.Background()
	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	jobs, err := backfiller.Store.ActiveJobs(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "auto", jobs[0].Source)
	mark, err := backfiller.Store.Watermark(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.True(t, mark.IsZero(), "the watermark must not move while the job is unfinished")

	// 失败的作业不推进水位：下一次扫描仍会覆盖同一窗口。
	invoker.fail = true
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	mark, _ = backfiller.Store.Watermark(ctx, "crypto", "ds", "1m")
	require.True(t, mark.IsZero(), "a failed job leaves the gap to be scanned again")

	// 成功后才推进。
	backfiller.lastScan = nil
	invoker.fail = false
	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	mark, _ = backfiller.Store.Watermark(ctx, "crypto", "ds", "1m")
	require.True(t, mark.After(base))

	// 扫描间隔内不重复扫描，也不叠加在未结束的自动作业上。
	before, _ := backfiller.Store.ActiveJobs(ctx, "crypto")
	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	after, _ := backfiller.Store.ActiveJobs(ctx, "crypto")
	require.Equal(t, len(before), len(after))
}

func TestAutoScanCatchUpAdvancesOneWindowAtATime(t *testing.T) {
	backfiller, _, _, base := newBackfillFixture(t)
	ctx := context.Background()
	// 夹具里计划从 base 起、当前时间是 base+48h：落后远超一个窗口时，一次扫描只覆盖 autoScanWindow，
	// 不会一次读出几十小时的行占满链路。
	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	jobs, err := backfiller.Store.ActiveJobs(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.True(t, jobs[0].From.Equal(base))
	require.Equal(t, autoScanWindow, jobs[0].To.Sub(jobs[0].From))
	require.True(t, jobs[0].AdvanceWatermark)
	require.Equal(t, "ds", jobs[0].DatasetID)
	require.Equal(t, "1m", jobs[0].Frequency)
}

func TestCheckRejectsOversizedRanges(t *testing.T) {
	backfiller, _, _, base := newBackfillFixture(t)
	_, err := backfiller.Check(context.Background(), Request{SpaceID: "crypto", From: base, To: base.AddDate(20, 0, 0)})
	require.ErrorContains(t, err, "narrow")
}

// step 推进一次调度（认领并执行一轮分块、结算上一轮已结束的作业）并等待分块执行完。
func step(t *testing.T, backfiller *Backfiller, times int) {
	t.Helper()
	for range times {
		require.NoError(t, backfiller.Tick(context.Background(), "crypto"))
		backfiller.Wait()
	}
}

func TestBackfillTreatsASingleBarMissingAtTheSourceAsDoneAndDoesNotResubmitIt(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	ctx := context.Background()
	invoker.noWrite = true
	// 单根缺口（ETH 第 10 分钟）：函数成功而仍然缺失，说明行情源没有这根 K 线。
	request := JobRequest{SpaceID: "crypto", SubjectID: "ETH-USDT", From: base, To: base.Add(time.Hour)}
	jobID, _, err := backfiller.Submit(ctx, request, "manual")
	require.NoError(t, err)
	step(t, backfiller, 2)
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)

	// 已确认的缺失写进库里：检查扣除它们，手动与自动补采都不再提交。
	result, err := backfiller.Check(ctx, Request{SpaceID: "crypto", SubjectID: "ETH-USDT", From: base, To: base.Add(time.Hour)})
	require.NoError(t, err)
	require.Zero(t, result.Missing)
	require.Equal(t, 1, result.Absent)
	for _, source := range []string{"manual", "auto"} {
		again, _, err := backfiller.Submit(ctx, request, source)
		require.NoError(t, err)
		require.Empty(t, again, source)
	}
	// 整段重拉不扣除。
	forced, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "ETH-USDT", From: base.Add(10 * time.Minute), To: base.Add(11 * time.Minute), Force: true}, "manual")
	require.NoError(t, err)
	require.NotEmpty(t, forced)

	// 清除后重新报告。
	cleared, err := backfiller.ClearAbsences(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.EqualValues(t, 1, cleared)
	result, err = backfiller.Check(ctx, Request{SpaceID: "crypto", SubjectID: "ETH-USDT", From: base, To: base.Add(time.Hour)})
	require.NoError(t, err)
	require.Equal(t, 1, result.Missing)
}

// 多根分块补完仍缺一部分：第一次退回重跑，第二次缺的完全一样才确认是行情源没有。
func TestBackfillConfirmsMultiBarSourceGapsAfterTwoIdenticalAttempts(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	ctx := context.Background()
	invoker.noWrite = true
	jobID, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)

	step(t, backfiller, 1)
	_, chunks, err := backfiller.Store.GetJob(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillPending, chunks[0].Status, "第一次缺失还不能确认，退回重跑")
	require.NotEmpty(t, chunks[0].Missing)

	step(t, backfiller, 2)
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
	_, chunks, _ = backfiller.Store.GetJob(ctx, jobID)
	require.Equal(t, "3 bars are absent at the source", chunks[0].LastError)
	result, err := backfiller.Check(ctx, Request{SpaceID: "crypto", SubjectID: "BTC-USDT", From: base, To: base.Add(time.Hour)})
	require.NoError(t, err)
	require.Zero(t, result.Missing)
	require.Equal(t, 3, result.Absent)
}

// 每次缺的不一样说明是暂时缺失：持续重跑直到补齐，不会确认为行情源没有。
func TestBackfillKeepsRetryingWhileTheMissingBarsChange(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	ctx := context.Background()
	invoker.healOne = true
	jobID, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	step(t, backfiller, 4)
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
	result, err := backfiller.Check(ctx, Request{SpaceID: "crypto", SubjectID: "BTC-USDT", From: base, To: base.Add(time.Hour)})
	require.NoError(t, err)
	require.Zero(t, result.Missing)
	require.Zero(t, result.Absent, "补齐的 K 线不会被记为行情源缺失")
}

func TestBackfillRetryRerunsFailedChunks(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	ctx := context.Background()
	invoker.fail = true
	jobID, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	step(t, backfiller, 2)
	status, _ := backfiller.Status(ctx, jobID)
	require.Equal(t, store.BackfillFailed, status.Job.Status)
	require.Len(t, status.Failed, 2)

	invoker.fail = false
	retried, err := backfiller.Retry(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, 2, retried)
	step(t, backfiller, 2)
	status, _ = backfiller.Status(ctx, jobID)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
	_, err = backfiller.Retry(ctx, jobID)
	require.ErrorIs(t, err, store.ErrJobNotRetryable)
}

func TestBackfillTreatsSourceEmptyFailureOfASingleBarAsAbsent(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	ctx := context.Background()
	invoker.sourceEmpty = true
	request := JobRequest{SpaceID: "crypto", SubjectID: "ETH-USDT", From: base, To: base.Add(time.Hour)}
	jobID, _, err := backfiller.Submit(ctx, request, "manual")
	require.NoError(t, err)
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
	auto, _, err := backfiller.Submit(ctx, request, "auto")
	require.NoError(t, err)
	require.Empty(t, auto)
}

// 纯符号映射变更（标的集合不变）没有数据集生效边界：变更之前的历史数据拒绝补采，之后的照常。
func TestDatasetMarketRefusesHistoryBeforeAMappingOnlyChange(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })

	base := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	options := marketfetch.PlanOptions{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-nanjing"}}, DefaultLatenessS: 15, Fetch: marketfetch.PlanFetch{Concurrency: 5}}
	build := func(previous *marketfetch.FetchPlanContent, external string) *marketfetch.FetchPlanContent {
		desired, err := marketfetch.BuildDesiredState(options, []marketfetch.TaskGroup{{
			Provider: "binance", SourceID: "binance", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds", Frequency: "1m",
			Subjects: []string{"BTC-USDT"}, ExternalSymbols: map[string]string{"BTC-USDT": external},
		}}, nil)
		require.NoError(t, err)
		var prevFunctions map[string]*marketfetch.PlanFunction
		var prevDatasets map[string]*marketfetch.PlanDatasetState
		if previous != nil {
			prevFunctions, prevDatasets = previous.Applied()
		}
		content, _, err := marketfetch.BuildPlanContent(prevFunctions, prevDatasets, desired, func(string) (int64, error) { return base.Unix(), nil })
		require.NoError(t, err)
		return content
	}
	first := build(nil, "BTCOLD")
	second := build(first, "BTCNEW")
	for _, content := range []*marketfetch.FetchPlanContent{first, second} {
		raw, err := json.Marshal(content)
		require.NoError(t, err)
		record, err := db.FetchPlans().Create(ctx, "crypto", string(raw), "{}")
		require.NoError(t, err)
		require.NoError(t, db.FetchPlans().Update(ctx, record.ID, store.FetchPlanApplied, "{}", ""))
	}
	backfiller := &Backfiller{Plans: db.FetchPlans(), BaseCtx: ctx, Now: func() time.Time { return base.Add(time.Hour) }}

	// 更新时间是“现在”：此前的历史数据拒绝补采。
	_, err = backfiller.datasetMarket(ctx, "crypto", "ds", "1m", "BTC-USDT", time.Now().Add(-time.Hour))
	require.Error(t, err)
	require.Contains(t, err.Error(), "without an effective boundary")
	// 变更之后的数据取新映射。
	plan, err := backfiller.datasetMarket(ctx, "crypto", "ds", "1m", "BTC-USDT", time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, "BTCNEW", plan.ExchangeSymbol("BTC-USDT"))

	// 变更所在的计划还没生效（进度仍在更新）：UpdatedAt 不是可靠边界，任何时间的补采都拒绝。
	raw, err := json.Marshal(build(second, "BTCNEWER"))
	require.NoError(t, err)
	_, err = db.FetchPlans().Create(ctx, "crypto", string(raw), "{}")
	require.NoError(t, err)
	_, err = backfiller.datasetMarket(ctx, "crypto", "ds", "1m", "BTC-USDT", time.Now().Add(time.Hour))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unfinished fetch plan")
}

// 开启补位后，分块执行完立刻认领下一块：一次调度就能处理完多块，且任何时刻都不超过并发上限。
func TestBackfillRefillDrainsAJobWithoutWaitingForTheNextTick(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	backfiller.MaxInFlight = 1
	backfiller.Refill = true
	ctx := context.Background()
	jobID, chunks, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: base, To: base.Add(2500 * time.Minute), Force: true}, "manual")
	require.NoError(t, err)
	require.Equal(t, 3, chunks)

	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.Len(t, invoker.events, 3, "补位让三块在一次调度内全部执行")
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
}

type failingInvoker struct{}

func (failingInvoker) Invoke(context.Context, string, string, map[string]any, cloudfunctionpb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	return scfinvoker.InvocationResult{}, errors.New("SCF must not be invoked")
}

type noFunctions struct{}

func (noFunctions) EnsureBackfillFunction(context.Context, string) (string, error) {
	return "", errors.New("the backfill function must not be created")
}

// 数据集走推送、补采执行端为 local：从推送项解析市场与交易所符号，调本地执行端，不建补采函数。
func TestBackfillUsesTheLocalExecutorForStreamDatasets(t *testing.T) {
	backfiller, local, _, base := newBackfillFixtureWithModes(t, map[string]marketfetch.DatasetMode{"ds": {Mode: marketfetch.IngestStreaming, Executor: marketfetch.PlanExecutorLocal}})
	backfiller.Invoker, backfiller.Local = failingInvoker{}, local
	backfiller.Functions = func(string) FunctionProvider { return noFunctions{} }
	backfiller.Executor = func(context.Context, string, string) (string, error) { return marketfetch.PlanExecutorLocal, nil }
	ctx := context.Background()
	jobID, chunks, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.Equal(t, 2, chunks)
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status, status.Failed)
	require.Len(t, local.events, 2)
	item := local.events[0]["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
	require.Contains(t, []any{"BTCUSDT", "ETHUSDT"}, item["symbol"])
}

// executor=scf 时仍调补采函数。
func TestBackfillUsesTheSCFFunctionWhenTheExecutorIsSCF(t *testing.T) {
	backfiller, scf, _, base := newBackfillFixture(t)
	backfiller.Local = failingInvoker{}
	backfiller.Executor = func(context.Context, string, string) (string, error) { return marketfetch.PlanExecutorSCF, nil }
	ctx := context.Background()
	_, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.Len(t, scf.events, 2)
}

// 补采作业成功后按（数据集, 频率）发补齐通知，带上确认行情源没有的标的。
func TestBackfillNotifiesFilledRangesAfterAJobSucceeds(t *testing.T) {
	backfiller, invoker, _, base := newBackfillFixture(t)
	invoker.noWrite = true
	var got []FilledRange
	backfiller.Notify = func(_ context.Context, filled []FilledRange) error { got = append(got, filled...); return nil }
	ctx := context.Background()
	jobID, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", DatasetID: "ds", SubjectID: "ETH-USDT", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	status, _ := backfiller.Status(ctx, jobID)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
	require.Len(t, got, 2)
	require.Equal(t, "ds", got[0].DatasetID)
	require.Equal(t, "1m", got[0].Frequency)
	require.Empty(t, got[0].Absent, "整个补齐范围不带确认缺失")
	require.False(t, got[0].From.After(base.Add(10*time.Minute)))
	// 确认没有的标的按库里的记录带出精确区间（单根缺口复查仍缺即确认行情源没有）。
	require.Equal(t, []string{"ETH-USDT"}, got[1].Absent)
	require.False(t, got[1].From.Before(got[0].From))
	require.False(t, got[1].To.After(got[0].To))
	require.Less(t, got[1].To.Sub(got[1].From), time.Hour)
}

// 恢复追赶：对暂停区间提交补采作业，补齐前没追平；补齐后追平；已确认缺失的不再补采并单独统计。
func TestCatchupSubmitsOneJobAndReportsWhenGapsAreFilled(t *testing.T) {
	backfiller, _, rows, base := newBackfillFixture(t)
	ctx := context.Background()

	state, err := backfiller.Catchup(ctx, "crypto", base, time.Time{})
	require.NoError(t, err)
	require.Positive(t, state.Missing)
	require.NotEmpty(t, state.JobID, "有缺口就提交追赶作业")
	require.False(t, state.CaughtUp())

	again, err := backfiller.Catchup(ctx, "crypto", base, time.Time{})
	require.NoError(t, err)
	require.Empty(t, again.JobID, "已有未结束的作业，不重复提交")
	require.Equal(t, 1, again.ActiveJobs)

	step(t, backfiller, 3)
	final, err := backfiller.Catchup(ctx, "crypto", base, time.Time{})
	require.NoError(t, err)
	require.Zero(t, final.Missing)
	require.True(t, final.CaughtUp(), "补齐且没有未结束的作业")
	_ = rows

	job, _, err := backfiller.Store.GetJob(ctx, state.JobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, job.Status)
}

func TestCatchupWithoutGapsIsImmediatelyCaughtUp(t *testing.T) {
	backfiller, _, rows, base := newBackfillFixture(t)
	rows.missing = map[string]map[int64]bool{"BTC-USDT": {}, "ETH-USDT": {}}
	state, err := backfiller.Catchup(context.Background(), "crypto", base, time.Time{})
	require.NoError(t, err)
	require.True(t, state.CaughtUp())
	require.Empty(t, state.JobID)
	require.Positive(t, state.Expected)
}

func TestBackfillerQuiesceWaitsForRunningChunks(t *testing.T) {
	backfiller, _, _, _ := newBackfillFixture(t)
	require.NoError(t, backfiller.Quiesce(context.Background()), "没有分块在执行")
	backfiller.workers.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, backfiller.Quiesce(ctx), context.DeadlineExceeded)
	backfiller.workers.Done()
	require.NoError(t, backfiller.Quiesce(context.Background()))
}

// 追赶目标是固定的：目标之后新出现的缺口（实时采集还没恢复）不算进这一轮，否则补采永远追不平。
func TestCatchupTargetIsFixed(t *testing.T) {
	backfiller, _, rows, base := newBackfillFixture(t)
	ctx := context.Background()
	// 固定目标早于夹具里的所有缺口（缺口在 base+3 分钟之后）：区间内没有缺口，立即追平。
	state, err := backfiller.Catchup(ctx, "crypto", base, base.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, state.CaughtUp())
	require.Empty(t, state.JobID)
	// 目标覆盖缺口时才提交作业。
	state, err = backfiller.Catchup(ctx, "crypto", base, base.Add(time.Hour))
	require.NoError(t, err)
	require.Positive(t, state.Missing)
	require.NotEmpty(t, state.JobID)
	_ = rows
}

// 暂停那一刻正在走的 K 线（开始时间早于暂停时间）是在暂停期间收盘的，恢复追赶要把它算进去。
func TestCatchupIncludesTheBarOpenAtPauseTime(t *testing.T) {
	backfiller, _, rows, base := newBackfillFixture(t)
	rows.missing = map[string]map[int64]bool{"BTC-USDT": {base.Add(time.Minute).Unix(): true}, "ETH-USDT": {}}
	since := base.Add(time.Minute + 30*time.Second) // 暂停时 base+1m 那根还没收盘
	ctx := context.Background()

	plain, err := backfiller.Checker.Check(ctx, Request{SpaceID: "crypto", From: since, To: base.Add(10 * time.Minute)})
	require.NoError(t, err)
	require.Zero(t, plain.Missing, "默认向上对齐：开始时间早于 From 的那根被漏掉")

	state, err := backfiller.Catchup(ctx, "crypto", since, base.Add(10*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, state.Missing, "恢复追赶包含暂停时正在走的那根")
	require.NotEmpty(t, state.JobID)
}

// 并发上限：默认 5（与 SCF 补采函数的预留并发一致）；MaxInFlightFor 按空间覆盖（全部本地执行端的空间可以放大）。
func TestMaxInFlightPerSpace(t *testing.T) {
	b := &Backfiller{}
	ctx := context.Background()
	require.Equal(t, DefaultMaxInFlight, b.maxInFlight(ctx, "crypto"))
	b.MaxInFlight = 7
	require.Equal(t, 7, b.maxInFlight(ctx, "crypto"))
	b.MaxInFlightFor = func(_ context.Context, space string) int {
		if space == "crypto" {
			return LocalMaxInFlight
		}
		return 0
	}
	require.Equal(t, LocalMaxInFlight, b.maxInFlight(ctx, "crypto"))
	require.Equal(t, 7, b.maxInFlight(ctx, "stockcn"), "返回 0 表示用默认")
}

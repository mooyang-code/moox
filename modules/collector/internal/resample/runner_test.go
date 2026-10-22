package resample

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type fakeSource struct{ subjects []string }

func (f fakeSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{SubjectTags: []string{"tag"}}, nil
}

func (f fakeSource) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	out := make([]domain.Subject, len(f.subjects))
	for i, id := range f.subjects {
		out[i] = domain.Subject{SubjectID: id}
	}
	return out, nil
}

type fakeSync struct {
	appended []string
	ready    bool
}

func (f *fakeSync) AppendDatasetSyncPoint(_ context.Context, _, _, requestID, _ string) error {
	f.appended = append(f.appended, requestID)
	return nil
}

func (f *fakeSync) WaitViewSyncPoint(context.Context, *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error) {
	return &storagepb.WaitViewSyncPointRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Ready: f.ready}, nil
}

// seedTask 在库里建一个就绪的合成任务，并给源数据集铺好 [from, to) 内每根 5m。
func seedTask(t *testing.T, mgr *store.Store, storage *fakeStorage, subjects []string, from, to time.Time) {
	t.Helper()
	params := `{"provider":"binance","market_type":"spot","source_dataset_id":"dataset_src","source_frequency":"5m","source_series_tag":"venue:binance","target_dataset_id":"dataset_task","target_frequency":"15m"}`
	require.NoError(t, mgr.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task", DataType: "kline_resample", CollectParams: params, Enabled: true,
		ResultDatasetID: "dataset_task", ResultViewID: "view_task", PrepareState: domain.PrepareStateReady,
	}))
	for at := from; at.Before(to); at = at.Add(5 * time.Minute) {
		for _, subject := range subjects {
			storage.putSource("crypto", "dataset_src", subject, "5m", "venue:binance", at, 1, 2, 0.5, 1, 1, 1, 1)
		}
	}
}

func TestFallbackFillsOnlyMissingSubjectsOfRecentClosedBuckets(t *testing.T) {
	mgr := openState(t)
	storage := newFakeStorage()
	now := utc("2026-10-21T10:36:30Z")
	seedTask(t, mgr, storage, []string{"BTC", "ETH"}, utc("2026-10-21T09:00:00Z"), utc("2026-10-21T10:30:00Z"))
	engine := &Engine{Primary: storage, State: mgr.Resample(), Now: func() time.Time { return now }}
	fallback := &Fallback{Tasks: mgr.Tasks(), Source: fakeSource{subjects: []string{"BTC", "ETH"}}, Engine: engine, Config: FallbackConfig{SpaceID: "crypto", LookbackBars: 3}}

	// 最近 3 个已收盘的桶（收盘不晚于 now−60s）：10:00、10:15 已收盘；10:30 的桶 10:45 才收盘。再往前是 09:45。
	require.NoError(t, fallback.Tick(context.Background(), now))
	for _, start := range []string{"2026-10-21T09:45:00Z", "2026-10-21T10:00:00Z", "2026-10-21T10:15:00Z"} {
		for _, subject := range []string{"BTC", "ETH"} {
			require.NotNil(t, storage.target("crypto", "dataset_task", subject, "15m", "venue:binance", utc(start)), "%s %s", subject, start)
		}
	}
	require.Nil(t, storage.target("crypto", "dataset_task", "BTC", "15m", "venue:binance", utc("2026-10-21T10:30:00Z")), "未收盘的桶不合成")
	require.Nil(t, storage.target("crypto", "dataset_task", "BTC", "15m", "venue:binance", utc("2026-10-21T09:30:00Z")), "超出最近 3 根的不管")

	// 再检查一次：已经有行，不再写。
	before := len(storage.upserts)
	require.NoError(t, fallback.Tick(context.Background(), now))
	require.Equal(t, before, len(storage.upserts), "兜底检查没有缺口时什么都不写")

	// 删掉一行，只补这一个标的。
	delete(storage.rows, keyText(rowKey("crypto", "dataset_task", "ETH", "15m", utc("2026-10-21T10:00:00Z"), "venue:binance")))
	require.NoError(t, fallback.Tick(context.Background(), now))
	last := storage.upserts[len(storage.upserts)-1]
	require.Len(t, last, 1)
	require.Equal(t, "ETH", last[0].GetKey().GetTimeSeries().GetSubjectId())
}

func TestFallbackSkipsGraceWindowAndUnpreparedTasks(t *testing.T) {
	mgr := openState(t)
	storage := newFakeStorage()
	seedTask(t, mgr, storage, []string{"BTC"}, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T10:30:00Z"))
	now := utc("2026-10-21T10:15:30Z") // 10:00 桶刚收盘 30 秒，在事件主路径的宽限内
	engine := &Engine{Primary: storage, State: mgr.Resample(), Now: func() time.Time { return now }}
	fallback := &Fallback{Tasks: mgr.Tasks(), Source: fakeSource{subjects: []string{"BTC"}}, Engine: engine, Config: FallbackConfig{SpaceID: "crypto", LookbackBars: 1}}
	require.NoError(t, fallback.Tick(context.Background(), now))
	require.Empty(t, storage.upserts, "收盘后 1 分钟内留给事件主路径")

	require.NoError(t, mgr.Tasks().SetPrepareState(context.Background(), "crypto", "task", domain.PrepareStateWaitingView, ""))
	require.NoError(t, fallback.Tick(context.Background(), now.Add(2*time.Minute)))
	require.Empty(t, storage.upserts, "没有就绪的任务不检查")
}

func submit(t *testing.T, mgr *store.Store, task *Task, start, end time.Time, subjects []string, now time.Time) store.ResampleJob {
	t.Helper()
	n := 0
	job, _, err := SubmitJob(context.Background(), mgr.Resample(), task, SubmitJobRequest{SpaceID: "crypto", TaskID: "task", RequestID: "req", Start: start, End: end, Subjects: subjects}, func() string {
		n++
		return fmt.Sprintf("job-%d", n)
	}, now)
	require.NoError(t, err)
	return job
}

func TestSubmitJobAlignsOutwardAndEnforcesBucketLimit(t *testing.T) {
	mgr := openState(t)
	task := makeTask(t, "crypto", "5m", "15m")
	now := utc("2026-10-22T00:00:00Z")
	job := submit(t, mgr, task, utc("2026-10-21T10:01:00Z"), utc("2026-10-21T10:31:00Z"), nil, now)
	require.Equal(t, utc("2026-10-21T10:00:00Z"), job.Start)
	require.Equal(t, utc("2026-10-21T10:45:00Z"), job.End, "向外对齐到目标桶：10:31 落在 10:30–10:45")
	require.Equal(t, 3, job.TotalBuckets)

	_, _, err := SubmitJob(context.Background(), mgr.Resample(), task, SubmitJobRequest{SpaceID: "crypto", TaskID: "task", RequestID: "big", Start: utc("2025-01-01T00:00:00Z"), End: utc("2026-01-01T00:00:00Z")}, func() string { return "x" }, now)
	require.ErrorContains(t, err, "more than")
	_, _, err = SubmitJob(context.Background(), mgr.Resample(), task, SubmitJobRequest{SpaceID: "crypto", TaskID: "task", RequestID: "bad", Start: now, End: now}, func() string { return "y" }, now)
	require.ErrorContains(t, err, "after start")
}

func TestJobRunnerBackfillsRangeWithCursorSyncAndSkips(t *testing.T) {
	mgr := openState(t)
	storage := newFakeStorage()
	seedTask(t, mgr, storage, []string{"BTC", "ETH"}, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T11:00:00Z"))
	// ETH 在 10:15 桶缺一根源数据。
	delete(storage.rows, keyText(rowKey("crypto", "dataset_src", "ETH", "5m", utc("2026-10-21T10:25:00Z"), "venue:binance")))
	now := utc("2026-10-21T12:00:00Z")
	engine := &Engine{Primary: storage, State: mgr.Resample(), Now: func() time.Time { return now }}
	sync := &fakeSync{ready: true}
	flushed := 0
	runner := &JobRunner{State: mgr.Resample(), Tasks: mgr.Tasks(), Source: fakeSource{subjects: []string{"BTC", "ETH"}}, Engine: engine, Sync: sync,
		Flush: func(context.Context) error { flushed++; return nil }, Config: JobRunnerConfig{BucketsPerRound: 2}}
	task := makeTask(t, "crypto", "5m", "15m")
	job := submit(t, mgr, task, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T11:00:00Z"), nil, now)
	require.Equal(t, 4, job.TotalBuckets)

	// 每轮最多推进 2 个桶：第一轮后游标在 10:30。
	require.NoError(t, runner.Tick(context.Background(), now))
	progress, err := mgr.Resample().GetJob(context.Background(), "crypto", job.JobID)
	require.NoError(t, err)
	require.Equal(t, store.ResampleJobRunning, progress.Status)
	require.Equal(t, 2, progress.DoneBuckets)
	require.Equal(t, utc("2026-10-21T10:30:00Z"), progress.NextBucket)

	require.NoError(t, runner.Tick(context.Background(), now))
	progress, _ = mgr.Resample().GetJob(context.Background(), "crypto", job.JobID)
	require.Equal(t, store.ResampleJobSyncing, progress.Status, "范围推进完成后进入同步")
	require.EqualValues(t, 7, progress.Written, "4 个桶 × 2 个标的，减去 ETH 跳过的 1 个")
	require.EqualValues(t, 1, progress.Skipped)
	require.Len(t, progress.SkippedSamples, 1)
	require.Contains(t, progress.SkippedSamples[0], "ETH")

	require.NoError(t, runner.Tick(context.Background(), now))
	progress, _ = mgr.Resample().GetJob(context.Background(), "crypto", job.JobID)
	require.Equal(t, store.ResampleJobSucceeded, progress.Status)
	require.Equal(t, []string{job.JobID}, sync.appended, "同步点的 request_id 是作业 ID")
	require.GreaterOrEqual(t, flushed, 1)

	// 历史作业的普通写入都记为补写通知，作业 ID 在通知里。
	notices, err := mgr.Resample().UnsealedNotices(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, notices, 7)
	require.Equal(t, job.JobID, notices[0].JobID)
	require.Equal(t, "dataset_task", notices[0].DatasetID)
}

func TestJobRunnerClipsToLatestClosedBucketAndHonorsCancelAndNotBefore(t *testing.T) {
	mgr := openState(t)
	storage := newFakeStorage()
	seedTask(t, mgr, storage, []string{"BTC"}, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T11:00:00Z"))
	now := utc("2026-10-21T10:32:00Z") // 10:30 的桶还没收盘
	engine := &Engine{Primary: storage, State: mgr.Resample(), Now: func() time.Time { return now }}
	runner := &JobRunner{State: mgr.Resample(), Tasks: mgr.Tasks(), Source: fakeSource{subjects: []string{"BTC"}}, Engine: engine}
	task := makeTask(t, "crypto", "5m", "15m")
	job := submit(t, mgr, task, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T11:00:00Z"), nil, now)
	require.NoError(t, runner.Tick(context.Background(), now))
	require.NoError(t, runner.Tick(context.Background(), now))
	progress, _ := mgr.Resample().GetJob(context.Background(), "crypto", job.JobID)
	require.Equal(t, store.ResampleJobSucceeded, progress.Status, "没有同步点存储时直接成功")
	require.EqualValues(t, 2, progress.Written, "只合成已收盘的 10:00 与 10:15 两个桶")
	require.Nil(t, storage.target("crypto", "dataset_task", "BTC", "15m", "venue:binance", utc("2026-10-21T10:30:00Z")))

	// 取消后不再推进。
	second := store.ResampleJob{JobID: "later", SpaceID: "crypto", TaskID: "task", RequestID: "later", Origin: store.ResampleOriginManual,
		Start: utc("2026-10-21T10:00:00Z"), End: utc("2026-10-21T10:15:00Z"), NotBefore: now.Add(time.Hour)}
	_, _, err := mgr.Resample().SubmitJob(context.Background(), second)
	require.NoError(t, err)
	require.NoError(t, runner.Tick(context.Background(), now))
	waiting, _ := mgr.Resample().GetJob(context.Background(), "crypto", "later")
	require.Equal(t, store.ResampleJobQueued, waiting.Status, "没到最早开始时间不开始")
	_, err = mgr.Resample().CancelJob(context.Background(), "crypto", "later")
	require.NoError(t, err)
	require.NoError(t, runner.Tick(context.Background(), now.Add(2*time.Hour)))
	cancelled, _ := mgr.Resample().GetJob(context.Background(), "crypto", "later")
	require.Equal(t, store.ResampleJobCancelled, cancelled.Status)
}

func TestJobRunnerFailsJobsWithoutTaskAndRetriesTransientErrors(t *testing.T) {
	mgr := openState(t)
	storage := newFakeStorage()
	seedTask(t, mgr, storage, []string{"BTC"}, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T10:30:00Z"))
	now := utc("2026-10-21T12:00:00Z")
	engine := &Engine{Primary: storage, State: mgr.Resample(), Now: func() time.Time { return now }}
	runner := &JobRunner{State: mgr.Resample(), Tasks: mgr.Tasks(), Source: fakeSource{subjects: []string{"BTC"}}, Engine: engine}
	task := makeTask(t, "crypto", "5m", "15m")

	storage.readErr = fmt.Errorf("storage down")
	job := submit(t, mgr, task, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T10:30:00Z"), nil, now)
	for i := 0; i < maxJobFailures-1; i++ {
		require.Error(t, runner.Tick(context.Background(), now))
	}
	progress, _ := mgr.Resample().GetJob(context.Background(), "crypto", job.JobID)
	require.Equal(t, store.ResampleJobRunning, progress.Status, "瞬时错误保留状态等待重试")
	require.Contains(t, progress.LastError, "storage down")
	storage.readErr = nil
	require.NoError(t, runner.Tick(context.Background(), now))
	require.NoError(t, runner.Tick(context.Background(), now))
	progress, _ = mgr.Resample().GetJob(context.Background(), "crypto", job.JobID)
	require.Equal(t, store.ResampleJobSucceeded, progress.Status)

	orphan := store.ResampleJob{JobID: "orphan", SpaceID: "crypto", TaskID: "gone", RequestID: "o", Start: utc("2026-10-21T10:00:00Z"), End: utc("2026-10-21T10:15:00Z"), NotBefore: now}
	_, _, err := mgr.Resample().SubmitJob(context.Background(), orphan)
	require.NoError(t, err)
	require.Error(t, runner.Tick(context.Background(), now))
	failed, _ := mgr.Resample().GetJob(context.Background(), "crypto", "orphan")
	require.Equal(t, store.ResampleJobFailed, failed.Status)
}

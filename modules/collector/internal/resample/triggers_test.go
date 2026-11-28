package resample

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
)

func TestPeriodTriggerResamplesBucketOnLastSourcePeriodOnly(t *testing.T) {
	mgr := openState(t)
	storage := newFakeStorage()
	seedTask(t, mgr, storage, []string{"BTC", "ETH", "SOL"}, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T10:30:00Z"))
	trigger := &PeriodTrigger{Index: &TaskIndex{Tasks: mgr.Tasks(), Source: fakeSource{subjects: []string{"BTC", "ETH", "SOL"}}}, Engine: &Engine{Primary: storage, State: mgr.Resample()}}

	// XRP 的源任务换过主来源：源行记在另一个 tag 下，与合成任务的 SourceSeriesTag 不同。
	for at := utc("2026-10-21T10:00:00Z"); at.Before(utc("2026-10-21T10:30:00Z")); at = at.Add(5 * time.Minute) {
		storage.putSource("crypto", "dataset_src", "XRP", "5m", "venue:okx", at, 1, 2, 0.5, 1, 1, 1, 1)
	}
	payload := func(period time.Time, status string, failed ...string) *storagepb.DatasetPeriodCompleted {
		return &storagepb.DatasetPeriodCompleted{
			DatasetId: "dataset_src", Frequency: "5m", PeriodTime: period.Unix(), Status: status,
			UniverseSubjectIds: []string{"BTC", "ETH", "SOL", "XRP"}, FailedSubjects: failed,
			Source: "ledger", SnapshotRef: "snapshot", CompletedAt: period.Add(6 * time.Minute).UnixMilli(),
		}
	}
	ctx := context.Background()
	// 10:00 桶（10:00–10:15）的前两根源 K 线事件不触发；最后一根（10:10）触发。SOL 在最后一根失败。
	require.NoError(t, trigger.OnPeriod(ctx, "crypto", payload(utc("2026-10-21T10:00:00Z"), "complete")))
	require.NoError(t, trigger.OnPeriod(ctx, "crypto", payload(utc("2026-10-21T10:05:00Z"), "complete")))
	require.Nil(t, storage.target("crypto", "dataset_task", "BTC", "15m", "venue:binance", utc("2026-10-21T10:00:00Z")), "不是桶内最后一根源周期，不触发")

	require.NoError(t, trigger.OnPeriod(ctx, "crypto", payload(utc("2026-10-21T10:10:00Z"), "degraded", "SOL")))
	require.NotNil(t, storage.target("crypto", "dataset_task", "BTC", "15m", "venue:binance", utc("2026-10-21T10:00:00Z")))
	require.NotNil(t, storage.target("crypto", "dataset_task", "ETH", "15m", "venue:binance", utc("2026-10-21T10:00:00Z")))
	require.Nil(t, storage.target("crypto", "dataset_task", "SOL", "15m", "venue:binance", utc("2026-10-21T10:00:00Z")), "degraded 事件里失败的标的先跳过，留给兜底")
	for _, tag := range []string{"venue:binance", "venue:okx"} {
		require.Nil(t, storage.target("crypto", "dataset_task", "XRP", "15m", tag, utc("2026-10-21T10:00:00Z")), "源 tag 与合成任务不同：读不到源窗口，跳过且不报错")
	}
}

func TestBackfillTriggerSubmitsSourceBackfillJobWithSettleDelay(t *testing.T) {
	mgr := openState(t)
	seedTask(t, mgr, newFakeStorage(), []string{"BTC"}, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T10:30:00Z"))
	confirmed := utc("2026-10-21T11:00:00Z")
	trigger := &BackfillTrigger{
		Index: &TaskIndex{Tasks: mgr.Tasks(), Source: fakeSource{subjects: []string{"BTC"}}}, State: mgr.Resample(), SettleAfter: time.Minute,
		Now: func() time.Time { return utc("2026-10-21T12:00:00Z") },
	}
	payload := &storagepb.CollectorKlineBackfilled{
		DatasetId: "dataset_src", Frequency: "5m", SubjectIds: []string{"BTC"}, Start: utc("2026-10-21T10:03:00Z").Unix(), End: utc("2026-10-21T10:20:00Z").Unix(),
		Origin: "auto", ConfirmedAt: confirmed.UnixMilli(),
	}
	for i := 0; i < 2; i++ { // 重复投递不重复建作业
		require.NoError(t, trigger.OnBackfilled(context.Background(), "crypto", "kbf-1", payload))
	}
	jobs, err := mgr.Resample().ListJobs(context.Background(), "crypto", "task", "", 10)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	job := jobs[0]
	require.Equal(t, store.ResampleOriginSourceBackfill, job.Origin)
	require.Equal(t, []string{"BTC"}, job.Subjects)
	require.Equal(t, utc("2026-10-21T10:00:00Z"), job.Start, "向外对齐到目标桶")
	require.Equal(t, utc("2026-10-21T10:30:00Z"), job.End)
	require.Equal(t, confirmed.Add(time.Minute), job.NotBefore, "最早开始时间 = 确认时刻 + 2×执行超时")
}

// brokenSource 对指定源数据集返回读取失败，其余走 fakeSource。
type brokenSource struct {
	fakeSource
	broken string
}

func (b brokenSource) GetDataset(ctx context.Context, spaceID, datasetID string) (storagesource.DatasetInfo, error) {
	if datasetID == b.broken {
		return storagesource.DatasetInfo{}, errors.New("dataset not found")
	}
	return b.fakeSource.GetDataset(ctx, spaceID, datasetID)
}

// 一个任务的源数据集读不到（已删但任务仍启用）只跳过它自己，同空间其余合成任务照常匹配事件。
func TestTaskIndexSkipsTaskWhoseSourceDatasetIsUnavailable(t *testing.T) {
	mgr := openState(t)
	seedTask(t, mgr, newFakeStorage(), nil, time.Time{}, time.Time{})
	params := `{"source_dataset_id":"dataset_gone","source_frequency":"5m","source_series_tag":"venue:binance","target_frequency":"15m"}`
	require.NoError(t, mgr.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "orphan", DataType: "kline_resample", CollectParams: params, Enabled: true,
		ResultDatasetID: "dataset_orphan", PrepareState: domain.PrepareStateReady,
	}))
	index := &TaskIndex{Tasks: mgr.Tasks(), Source: brokenSource{broken: "dataset_gone"}}

	tasks, err := index.Match(context.Background(), "crypto", "dataset_src", "5m")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "task", tasks[0].Spec.TaskID)
}

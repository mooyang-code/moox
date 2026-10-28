package resample

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPeriodTriggerResamplesBucketOnLastSourcePeriodOnly(t *testing.T) {
	mgr := openState(t)
	storage := newFakeStorage()
	seedTask(t, mgr, storage, []string{"BTC", "ETH", "SOL"}, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T10:30:00Z"))
	trigger := &PeriodTrigger{Index: &TaskIndex{Tasks: mgr.Tasks()}, Engine: &Engine{Primary: storage, State: mgr.Resample()}}

	payload := func(period time.Time, status string, failed ...string) *storagepb.DatasetPeriodCompleted {
		return &storagepb.DatasetPeriodCompleted{
			DatasetId: "dataset_src", Frequency: "5m", PeriodTime: period.Unix(), Status: status,
			UniverseSubjectIds: []string{"BTC", "ETH", "SOL"}, FailedSubjects: failed, Source: "ledger", SnapshotRef: "snapshot", CompletedAt: timestamppb.New(period.Add(6 * time.Minute)),
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
}

func TestBackfillTriggerSubmitsSourceBackfillJobWithSettleDelay(t *testing.T) {
	mgr := openState(t)
	seedTask(t, mgr, newFakeStorage(), []string{"BTC"}, utc("2026-10-21T10:00:00Z"), utc("2026-10-21T10:30:00Z"))
	confirmed := utc("2026-10-21T11:00:00Z")
	trigger := &BackfillTrigger{
		Index: &TaskIndex{Tasks: mgr.Tasks()}, State: mgr.Resample(), SettleAfter: time.Minute,
		Now: func() time.Time { return utc("2026-10-21T12:00:00Z") },
	}
	payload := &storagepb.CollectorKlineBackfilled{
		DatasetId: "dataset_src", Frequency: "5m", SubjectIds: []string{"BTC"}, Start: timestamppb.New(utc("2026-10-21T10:03:00Z")), End: timestamppb.New(utc("2026-10-21T10:20:00Z")),
		Origin: "auto", SyncRequestId: "kbf-1", ConfirmedAt: timestamppb.New(confirmed),
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

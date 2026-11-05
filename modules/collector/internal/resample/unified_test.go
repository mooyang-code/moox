package resample

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepbgen "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	unifiedSpotTag = "venue:binance|market:spot|source:spot_http"
	unifiedSwapTag = "venue:binance|market:swap|source:swap_http"
)

// unifiedSource 是统一数据集的源：现货标签有 BTC、ETH，永续标签只有 BTC。
type unifiedSource struct{}

func (unifiedSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{SubjectTags: []string{"tag_spot", "tag_swap"}}, nil
}

func (unifiedSource) GetTag(_ context.Context, _, tagID string) (*storagepbgen.Tag, error) {
	market := map[string]string{"tag_spot": "spot", "tag_swap": "swap"}[tagID]
	return &storagepbgen.Tag{TagId: tagID, Attributes: map[string]string{"market_type": market, "sync.source": "binance"}}, nil
}

func (unifiedSource) ResolveSubjects(_ context.Context, _ string, tagIDs []string) ([]domain.Subject, error) {
	members := map[string][]string{"tag_spot": {"BTC", "ETH"}, "tag_swap": {"BTC"}}
	var out []domain.Subject
	for _, tagID := range tagIDs {
		for _, id := range members[tagID] {
			out = append(out, domain.Subject{SubjectID: id})
		}
	}
	return out, nil
}

func makeUnifiedTask(t *testing.T) *Task {
	t.Helper()
	params := `{"provider":"binance","market_type":"multi","source_dataset_id":"dataset_src","source_frequency":"1m","source_series_tags":["` + unifiedSpotTag + `","` + unifiedSwapTag + `"],"target_dataset_id":"dataset_task","target_frequency":"5m"}`
	task, err := NewTask(domain.CollectionTask{SpaceID: "crypto", TaskID: "task", DataType: "kline_resample", CollectParams: params, Enabled: true, ResultDatasetID: "dataset_task", ResultViewID: "view_task"})
	require.NoError(t, err)
	return task
}

func TestSourceMembersTakesEachMarketsOwnMembers(t *testing.T) {
	task := makeUnifiedTask(t)
	members, err := SourceMembers(context.Background(), unifiedSource{}, task)
	require.NoError(t, err)
	require.Equal(t, Members{unifiedSpotTag: {"BTC", "ETH"}, unifiedSwapTag: {"BTC"}}, members, "按市场各取自己标签的成员，不取并集")
	require.Equal(t, 3, members.Count())
}

// 统一数据集 1m 合成 5m：现货、永续各一条序列；快照与周期提交里没有不存在的序列（ETH 没有永续）。
func TestUnifiedDatasetResamplesEachSeriesAndNeverInventsOne(t *testing.T) {
	mgr := openState(t)
	storage := newFakeStorage()
	registrar := &fakeRegistrar{}
	now := utc("2026-10-21T10:03:00Z")
	engine := &Engine{Primary: storage, Period: storage, Snapshots: registrar, State: mgr.Resample(), Now: func() time.Time { return now }}
	task := makeUnifiedTask(t)
	ctx := context.Background()

	members, err := SourceMembers(ctx, unifiedSource{}, task)
	require.NoError(t, err)
	require.NoError(t, engine.EnsureSnapshot(ctx, task, members))
	require.Len(t, registrar.calls, 1)
	latest, ok, err := mgr.Resample().LatestSnapshot(ctx, "crypto", "task")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"BTC", "ETH"}, latest.Subjects)
	require.Equal(t, map[string][]string(members), latest.Members)

	// 源：每个序列 5 根 1m K 线，价格不同以区分序列。
	bucket, _, err := task.Clock.BucketAt(utc("2026-10-21T10:05:00Z"))
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		at := bucket.Start.Add(time.Duration(i) * time.Minute)
		storage.putSource("crypto", "dataset_src", "BTC", "1m", unifiedSpotTag, at, 10, 12, 9, 11, 1, 1, 1)
		storage.putSource("crypto", "dataset_src", "ETH", "1m", unifiedSpotTag, at, 20, 22, 19, 21, 1, 1, 1)
		storage.putSource("crypto", "dataset_src", "BTC", "1m", unifiedSwapTag, at, 30, 32, 29, 31, 1, 1, 1)
	}
	// 快照要先生效：把生效点放到桶之前。
	require.NoError(t, mgr.Resample().AddSnapshot(ctx, store.ResampleSnapshot{
		SpaceID: "crypto", TaskID: "task", EffectiveFrom: bucket.Key.Add(-time.Hour).Unix(), SeriesHash: latest.SeriesHash,
		Subjects: latest.Subjects, Members: latest.Members,
	}))
	engine.Now = func() time.Time { return bucket.End.Add(10 * time.Second) }

	result, err := engine.RunBucketMembers(ctx, task, bucket, members, "")
	require.NoError(t, err)
	require.Equal(t, 3, result.Written)
	require.Equal(t, 3, result.Committed, "三条序列都落在快照内，用周期提交")
	require.Empty(t, result.Skipped)
	require.Len(t, storage.commits, 2, "每条源序列各提交一次，共用同一份快照哈希")
	for _, commit := range storage.commits {
		require.Equal(t, latest.SeriesHash, commit.GetSeriesHash())
		require.EqualValues(t, 3, commit.GetExpectedCount(), "全集只有 3 条序列")
	}

	require.Equal(t, 11.0, closeOf(storage.target("crypto", "dataset_task", "BTC", "5m", unifiedSpotTag, bucket.Key)))
	require.Equal(t, 21.0, closeOf(storage.target("crypto", "dataset_task", "ETH", "5m", unifiedSpotTag, bucket.Key)))
	require.Equal(t, 31.0, closeOf(storage.target("crypto", "dataset_task", "BTC", "5m", unifiedSwapTag, bucket.Key)))
	require.Nil(t, storage.target("crypto", "dataset_task", "ETH", "5m", unifiedSwapTag, bucket.Key), "ETH 没有永续，不生成不存在的序列")
}

// 永续失败、现货成功：事件的序列名单里失败的只有永续，现货照常合成。
func TestUnifiedPeriodTriggerSkipsOnlyFailedSeries(t *testing.T) {
	mgr := openState(t)
	storage := newFakeStorage()
	params := `{"provider":"binance","market_type":"multi","source_dataset_id":"dataset_src","source_frequency":"1m","source_series_tags":["` + unifiedSpotTag + `","` + unifiedSwapTag + `"],"target_dataset_id":"dataset_task","target_frequency":"5m"}`
	require.NoError(t, mgr.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task", DataType: "kline_resample", CollectParams: params, Enabled: true,
		ResultDatasetID: "dataset_task", ResultViewID: "view_task", PrepareState: domain.PrepareStateReady,
	}))
	start := utc("2026-10-21T10:00:00Z")
	for i := 0; i < 5; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		for _, tag := range []string{unifiedSpotTag, unifiedSwapTag} {
			storage.putSource("crypto", "dataset_src", "BTC", "1m", tag, at, 1, 2, 0.5, 1, 1, 1, 1)
		}
	}
	trigger := &PeriodTrigger{Index: &TaskIndex{Tasks: mgr.Tasks()}, Engine: &Engine{Primary: storage, State: mgr.Resample()}}
	last := start.Add(4 * time.Minute)
	err := trigger.OnPeriod(context.Background(), "crypto", &storagepb.DatasetPeriodCompleted{
		DatasetId: "dataset_src", Frequency: "1m", PeriodTime: last.Unix(), Status: "degraded", Source: "ledger", SnapshotRef: "snapshot",
		UniverseSubjectIds: []string{"BTC"}, FailedSubjects: []string{"BTC"},
		UniverseSeries: []*storagepb.SeriesRef{{SubjectId: "BTC", SeriesTag: unifiedSpotTag}, {SubjectId: "BTC", SeriesTag: unifiedSwapTag}},
		FailedSeries:   []*storagepb.SeriesRef{{SubjectId: "BTC", SeriesTag: unifiedSwapTag}},
		CompletedAt:    timestamppb.New(last.Add(time.Minute)),
	})
	require.NoError(t, err)
	require.NotNil(t, storage.target("crypto", "dataset_task", "BTC", "5m", unifiedSpotTag, start), "现货照常合成")
	require.Nil(t, storage.target("crypto", "dataset_task", "BTC", "5m", unifiedSwapTag, start), "失败的永续序列先跳过，留给兜底")
}

// 补采只写了永续：作业只合成永续；补采只写了任务不合成的序列：不建作业。
func TestBackfillTriggerFiltersBySeriesTags(t *testing.T) {
	mgr := openState(t)
	params := `{"provider":"binance","market_type":"multi","source_dataset_id":"dataset_src","source_frequency":"1m","source_series_tags":["` + unifiedSpotTag + `","` + unifiedSwapTag + `"],"target_dataset_id":"dataset_task","target_frequency":"5m"}`
	require.NoError(t, mgr.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task", DataType: "kline_resample", CollectParams: params, Enabled: true,
		ResultDatasetID: "dataset_task", ResultViewID: "view_task", PrepareState: domain.PrepareStateReady,
	}))
	trigger := &BackfillTrigger{Index: &TaskIndex{Tasks: mgr.Tasks()}, State: mgr.Resample(), Now: func() time.Time { return utc("2026-10-21T12:00:00Z") }}
	payload := func(tags ...string) *storagepb.CollectorKlineBackfilled {
		return &storagepb.CollectorKlineBackfilled{
			DatasetId: "dataset_src", Frequency: "1m", SubjectIds: []string{"BTC"}, SeriesTags: tags,
			Start: timestamppb.New(utc("2026-10-21T10:00:00Z")), End: timestamppb.New(utc("2026-10-21T10:10:00Z")),
			Origin: "auto", SyncRequestId: "kbf", ConfirmedAt: timestamppb.New(utc("2026-10-21T11:00:00Z")),
		}
	}
	ctx := context.Background()
	require.NoError(t, trigger.OnBackfilled(ctx, "crypto", "kbf-other", payload("venue:okx|market:swap")))
	jobs, err := mgr.Resample().ListJobs(ctx, "crypto", "task", "", 10)
	require.NoError(t, err)
	require.Empty(t, jobs)

	require.NoError(t, trigger.OnBackfilled(ctx, "crypto", "kbf-swap", payload(unifiedSwapTag)))
	jobs, err = mgr.Resample().ListJobs(ctx, "crypto", "task", "", 10)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, []string{unifiedSwapTag}, jobs[0].SeriesTags)

	// 单序列的补采事件（不带 series_tags）= 全部序列：合并后作业不再限定序列。
	require.NoError(t, trigger.OnBackfilled(ctx, "crypto", "kbf-all", payload()))
	jobs, err = mgr.Resample().ListJobs(ctx, "crypto", "task", "", 10)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Empty(t, jobs[0].SeriesTags)

	// 作业按序列限定成员。
	runner := &JobRunner{Source: unifiedSource{}}
	task := makeUnifiedTask(t)
	members, err := runner.jobMembers(ctx, task, store.ResampleJob{SeriesTags: []string{unifiedSwapTag}, Subjects: []string{"BTC", "ETH"}})
	require.NoError(t, err)
	require.Equal(t, Members{unifiedSwapTag: {"BTC"}}, members)
}

// 补写通知记下序列，合并成批后事件带 series_tags；单序列数据集的事件不带。
func TestNoticePublisherCarriesSeriesTagsOnlyForUnifiedDatasets(t *testing.T) {
	mgr := openState(t)
	ctx := context.Background()
	start := utc("2026-10-21T10:00:00Z")
	require.NoError(t, mgr.Resample().AddNotices(ctx, []store.BackfillNotice{
		{SpaceID: "crypto", DatasetID: "dataset_a", Frequency: "5m", SubjectID: "BTC", SeriesTag: unifiedSwapTag, Start: start, End: start.Add(time.Hour), Origin: "resample", JobID: "job-1"},
		{SpaceID: "crypto", DatasetID: "dataset_a", Frequency: "5m", SubjectID: "ETH", SeriesTag: unifiedSpotTag, Start: start, End: start.Add(time.Hour), Origin: "resample", JobID: "job-1"},
		{SpaceID: "crypto", DatasetID: "dataset_b", Frequency: "5m", SubjectID: "BTC", Start: start, End: start.Add(time.Hour), Origin: "resample", JobID: "job-2"},
	}))
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	relay := &recordingRelay{}
	publisher := &NoticePublisher{State: mgr.Resample(), Registry: registry, Relay: relay, Now: func() time.Time { return start.Add(3 * time.Hour) }}
	require.NoError(t, publisher.Flush(ctx))
	require.Len(t, relay.messages, 2)
	byJob := map[string]*storagepb.CollectorKlineBackfilled{}
	for _, message := range relay.messages {
		value := decodePayload(t, registry, message)
		byJob[value.GetJobId()] = value
	}
	require.Equal(t, []string{unifiedSpotTag, unifiedSwapTag}, byJob["job-1"].GetSeriesTags())
	require.Empty(t, byJob["job-2"].GetSeriesTags(), "单序列数据集的事件不带 series_tags")
}

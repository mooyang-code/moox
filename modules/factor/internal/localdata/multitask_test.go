package localdata

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

// 同一源数据集上的两个任务（例如两个策略实例的私有任务）共用一个数据源：按标的并集与最大回看准备，
// 各自的预热状态按自己的标的统计，各自都能算；释放一个任务后数据源按其余任务收缩。
func TestTwoTasksShareOneSource(t *testing.T) {
	fx := newFixture(t, mean3())
	second := domain.EngineTask{Set: fx.set.Set, Members: domain.AsMembers(mean5())}
	second.Set.TaskID, second.Set.ResultDatasetID = "set2", "factor_spot_2"
	for _, subject := range []string{"BTC", "ETH"} {
		fx.st.put(subject, 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	}

	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	require.NoError(t, fx.data.Prepare(context.Background(), second, []string{"ETH"}))
	require.Equal(t, []pipeline.WarmupStatus{
		{TaskID: "set1", State: pipeline.WarmupReady, WarmSubjects: 1, ExpectedSubjects: 1},
		{TaskID: "set2", State: pipeline.WarmupReady, WarmSubjects: 1, ExpectedSubjects: 1},
	}, fx.data.WindowStatuses())
	// 回看取两个任务里较大的 5 根：ETH 本地有 5 根。
	bounds, ok, err := fx.data.Store().Bounds(seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}, "ETH")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at(5), bounds.Earliest)

	require.Equal(t, "complete", fx.live(t, at(9), []string{"BTC"}).Status)
	require.InDelta(t, (107+108+109)/3.0, fx.result("BTC", 9).Fields["mean"], 1e-9)
	plan, err := pipeline.BuildLivePlan(periodclock.Continuous{}, pipeline.LiveInput{
		Set: second.Set, Members: second.Members, PeriodTime: at(9), UniverseSubjects: []string{"ETH"}, CarryColumns: []string{"close", "volume"}, TriggerEventID: "evt",
	})
	require.NoError(t, err)
	outcome, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	fx.data.Drain()
	require.Equal(t, "complete", outcome.Status)
	require.InDelta(t, (105+106+107+108+109)/5.0, fx.result("ETH", 9).Fields["mean5"], 1e-9)

	fx.data.ReleaseTask(second.Set)
	statuses := fx.data.WindowStatuses()
	require.Len(t, statuses, 1)
	require.Equal(t, "set1", statuses[0].TaskID)

	// 最后一个任务也释放后，数据源的窗口与同步状态被丢弃（不再占内存预算、不再被跟进）。
	fx.data.ReleaseTask(fx.set.Set)
	require.Empty(t, fx.data.WindowStatuses())
	require.Nil(t, fx.data.st(seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}))
}

// RetainTasks 清理已删除或预载失败残留的任务登记：
// - 源数据集仍被其他活跃任务共享时，只移除已删任务登记，保留共享源数据集与窗口；
// - 源数据集不再被任何活跃任务引用时，释放其窗口、内存预算与拉取状态。
func TestRetainTasksPreservesSharedSourceAndDropsUnreferenced(t *testing.T) {
	fx := newFixture(t, mean3())
	shared := domain.EngineTask{Set: fx.set.Set, Members: domain.AsMembers(mean3())}
	shared.Set.TaskID, shared.Set.ResultDatasetID = "set_shared", "factor_spot_shared"

	other := domain.EngineTask{
		Set: domain.FactorTask{
			TaskID: "set_other", SpaceID: "crypto", SourceDatasetID: "perp", Freq: "1m", ResultDatasetID: "factor_perp",
		},
		Members: domain.AsMembers(mean3()),
	}

	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	fx.st.put("ETH", 200, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)

	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	require.NoError(t, fx.data.Prepare(context.Background(), shared, []string{"ETH"}))
	require.NoError(t, fx.data.Prepare(context.Background(), other, []string{"BTC"}))

	spotSrc := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	perpSrc := seriesstore.Source{SpaceID: "crypto", DatasetID: "perp", Freq: "1m"}

	// 删除 set1（与 set_shared 共享 spot）和 set_other（独占 perp）：
	// spot 应继续保留给 set_shared，perp 应被彻底释放。
	dropped := fx.data.RetainTasks(map[string]struct{}{"set_shared": {}})
	require.Equal(t, []seriesstore.Source{perpSrc}, dropped)
	require.NotNil(t, fx.data.st(spotSrc), "共享源数据集不能被误删")
	require.Nil(t, fx.data.st(perpSrc), "无活跃任务引用的源数据集应被释放")
	require.Equal(t, []SourceInfo{{Source: spotSrc, TaskID: "set_shared"}}, fx.data.Sources())

	// 删除最后一个任务后，spot 也被彻底释放。
	dropped = fx.data.RetainTasks(map[string]struct{}{})
	require.Equal(t, []seriesstore.Source{spotSrc}, dropped)
	require.Nil(t, fx.data.st(spotSrc))
	require.Empty(t, fx.data.Sources())
}

// 并发竞态回归测试：Prepare 与 RetainTasks/ReleaseTask 同时操作同一个 src 时，
// 被 dropSourceLocked 移除的旧 *source 会标记 dropped=true，Prepare 必须重试获取活跃实例，
// 不得把新任务写入孤立 *source 导致永久 Warming 或 Admit 常驻配额泄漏。
func TestConcurrentPrepareAndRetainReleaseNoOrphanedSource(t *testing.T) {
	st := &fakeStorage{rows: map[string][]bar{}}
	st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	st.put("ETH", 200, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)

	store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 4 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	admit := memadmit.New(64 << 20)
	data := New(Config{
		Store:   store,
		Storage: st,
		Admit:   admit,
		Now:     func() time.Time { return at(10) },
	})
	t.Cleanup(data.Drain)

	taskA := domain.EngineTask{
		Set:     domain.FactorTask{TaskID: "set_a", SpaceID: "crypto", SourceDatasetID: "spot", Freq: "1m", ResultDatasetID: "factor_a"},
		Members: domain.AsMembers(mean3()),
	}
	taskB := domain.EngineTask{
		Set:     domain.FactorTask{TaskID: "set_b", SpaceID: "crypto", SourceDatasetID: "spot", Freq: "1m", ResultDatasetID: "factor_b"},
		Members: domain.AsMembers(mean3()),
	}
	spotSrc := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	ctx := context.Background()

	// 反复并发交错：先让 taskA 占据 spot，再并发 Prepare(taskB) + ReleaseTask(taskA) + RetainTasks({taskB})。
	for i := 0; i < 40; i++ {
		require.NoError(t, data.Prepare(ctx, taskA, []string{"BTC"}))

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(3)
		var prepErr error
		go func() {
			defer wg.Done()
			<-start
			prepErr = data.Prepare(ctx, taskB, []string{"ETH"})
		}()
		go func() {
			defer wg.Done()
			<-start
			data.ReleaseTask(taskA.Set)
		}()
		go func() {
			defer wg.Done()
			<-start
			data.RetainTasks(map[string]struct{}{"set_b": {}})
		}()
		close(start)
		wg.Wait()
		require.NoError(t, prepErr)

		// 并发完成后：spot 必须在 d.sources 中、taskB 必须处于 Ready，不能落入孤立 *source。
		live := data.st(spotSrc)
		require.NotNil(t, live, "iter=%d: taskB Prepare 成功后数据源必须仍在 d.sources 中", i)
		require.Equal(t, []SourceInfo{{Source: spotSrc, TaskID: "set_b"}}, data.Sources())
		require.Equal(t, []pipeline.WarmupStatus{
			{TaskID: "set_b", State: pipeline.WarmupReady, WarmSubjects: 1, ExpectedSubjects: 1},
		}, data.WindowStatuses())
		require.Greater(t, admit.Stats().Resident, int64(0), "iter=%d: 活跃窗口必须登记在 Admit 预算中", i)

		// 清理全部任务后，Admit 常驻配额必须归零，无任何泄漏。
		data.RetainTasks(map[string]struct{}{})
		require.Nil(t, data.st(spotSrc))
		require.Zero(t, admit.Stats().Resident, "iter=%d: 释放全部任务后 Admit 常驻内存必须归零", i)
	}
}

package localdata

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

// 同一源数据集上的两个集合（例如两个策略实例的私有集合）共用一个数据源：按标的并集与最大回看准备，
// 各自的预热状态按自己的标的统计，各自都能算；释放一个集合后数据源按其余集合收缩。
func TestTwoSetsShareOneSource(t *testing.T) {
	fx := newFixture(t, mean3())
	second := domain.EngineSet{Set: fx.set.Set, Members: domain.AsMembers(mean5())}
	second.Set.SetID, second.Set.ResultDatasetID = "set2", "factor_spot_2"
	for _, subject := range []string{"BTC", "ETH"} {
		fx.st.put(subject, 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	}

	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	require.NoError(t, fx.data.Prepare(context.Background(), second, []string{"ETH"}))
	require.Equal(t, []pipeline.WarmupStatus{
		{SetID: "set1", State: pipeline.WarmupReady, WarmSubjects: 1, ExpectedSubjects: 1},
		{SetID: "set2", State: pipeline.WarmupReady, WarmSubjects: 1, ExpectedSubjects: 1},
	}, fx.data.WindowStatuses())
	// 回看取两个集合里较大的 5 根：ETH 本地有 5 根。
	bounds, ok, err := fx.data.Store().Bounds(seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}, "ETH")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at(5), bounds.Earliest)

	require.Equal(t, "complete", fx.live(t, at(9), []string{"BTC"}).Status)
	require.InDelta(t, (107+108+109)/3.0, fx.result("BTC", 9).Fields["mean3"], 1e-9)
	plan, err := pipeline.BuildLivePlan(periodclock.Continuous{}, pipeline.LiveInput{
		Set: second.Set, Members: second.Members, PeriodTime: at(9), Universe: []string{"ETH"}, CarryColumns: []string{"close", "volume"}, TriggerEventID: "evt",
	})
	require.NoError(t, err)
	outcome, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	fx.data.Drain()
	require.Equal(t, "complete", outcome.Status)
	require.InDelta(t, (105+106+107+108+109)/5.0, fx.result("ETH", 9).Fields["mean5"], 1e-9)

	fx.data.ReleaseSet(second.Set)
	statuses := fx.data.WindowStatuses()
	require.Len(t, statuses, 1)
	require.Equal(t, "set1", statuses[0].SetID)

	// 最后一个集合也释放后，数据源的窗口与同步状态被丢弃（不再占内存预算、不再被跟进）。
	fx.data.ReleaseSet(fx.set.Set)
	require.Empty(t, fx.data.WindowStatuses())
	require.Nil(t, fx.data.st(seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}))
}

package localdata

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// 连续水位判断“这一期哪些标的到了”：先增量同步到本地，再按本地这一期有没有真实的行判断，不按本地最新时间。
// BTC 第 10 根在 Storage 里缺、第 11 根已到：第 10 期不算到；同步到第 11 根时第 10 根补成填充行，仍不算到，第 11 期算到。
// SOL 是新进名单的标的（本地没有数据）：先纳入同步，第 11 期也算到。
func TestRealRowsAtSyncsFirstAndIgnoresFilledRows(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	fx.st.put("ETH", 200, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC", "ETH"}))
	fx.st.put("BTC", 100, 11)
	fx.st.put("ETH", 200, 10, 11)
	fx.st.put("SOL", 300, 9, 10, 11)
	fx.now = at(12)
	subjects := []string{"BTC", "ETH", "SOL"}

	got, err := fx.data.RealRowsAt(context.Background(), fx.set.Set, subjects, at(10))
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"ETH": true, "SOL": true}, got, "BTC 第 10 根还没有")

	got, err = fx.data.RealRowsAt(context.Background(), fx.set.Set, subjects, at(11))
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"BTC": true, "ETH": true, "SOL": true}, got)
	require.Equal(t, at(11), fx.tagLatest(t, "SOL", plainTag), "新进名单的标的已同步到本地")

	got, err = fx.data.RealRowsAt(context.Background(), fx.set.Set, subjects, at(10))
	require.NoError(t, err)
	require.False(t, got["BTC"], "第 10 根已补成填充行，不按本地最新时间算到")
}

// 数据源还没准备好（窗口没载入）时不判断：返回基础设施错误，由连续水位稍后重试。
func TestRealRowsAtRequiresAPreparedSource(t *testing.T) {
	fx := newFixture(t, mean3())
	_, err := fx.data.RealRowsAt(context.Background(), fx.set.Set, []string{"BTC"}, at(10))
	require.ErrorIs(t, err, storageio.ErrInfra)
}

// 没有期望序列的读入（试算先读入、再按读到的数据补期望序列）：本地有数据的标的不判失败。
func TestLoadSubjectsWithoutExpectedSeriesKeepsSubjectsAvailable(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, TargetStart: at(7), TargetEnd: at(10),
		Expected: []string{"BTC"}, Available: []string{"BTC"}}
	plan, loaded, err := fx.runner.LoadSubjects(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"BTC"}, loaded.Available)
	require.Equal(t, plainSeries("BTC"), plan.ExpectedSeries)
}

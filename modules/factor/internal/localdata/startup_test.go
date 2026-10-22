package localdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

var startupSrc = seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}

// 本地和 Storage 都没有任何数据的标的：数据源未就绪，进程不退出，列出这些标的（设计 6.2，决定 24）。
func TestPrepareFailsStrictlyWhenSubjectHasNoDataAnywhere(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)

	err := fx.data.Prepare(context.Background(), fx.set, []string{"BTC", "GHOST"})
	var noData *NoDataError
	require.True(t, errors.As(err, &noData), "得到 %v", err)
	require.Equal(t, []string{"GHOST"}, noData.Subjects)
	require.Contains(t, noData.Error(), "GHOST")
	require.Equal(t, noData, fx.data.Blocked(startupSrc), "未就绪原因要能被生命周期检查读到")
	for _, status := range fx.data.WindowStatuses() {
		require.NotEqual(t, "ready", string(status.State), "有零数据标的时窗口不能处于就绪")
	}

	// 导入（或采集）补上数据后，再准备就恢复。
	fx.st.put("GHOST", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC", "GHOST"}))
	require.NoError(t, fx.data.Blocked(startupSrc))
}

// 因子的回看不能超过数据源的回看数量 N。
func TestPrepareRejectsFactorLookbackBeyondN(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.data.cfg.Lookback = func(seriesstore.Source) int { return 2 } // mean3 需要 3 根
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)

	err := fx.data.Prepare(context.Background(), fx.set, []string{"BTC"})
	var over *LookbackExceededError
	require.True(t, errors.As(err, &over), "得到 %v", err)
	require.Equal(t, 2, over.N)
	require.Equal(t, 3, over.Max)
	require.Equal(t, []string{"mean"}, over.Factors)

	fx.data.cfg.Lookback = func(seriesstore.Source) int { return 3 } // 恰好等于回看：通过
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
}

// 窗口长度就是 N，而不是集合里因子的最大回看。
func TestWindowLengthIsConfiguredLookback(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.data.cfg.Lookback = func(seriesstore.Source) int { return 8 }
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	window := fx.data.st(startupSrc).win.Load()
	require.NotNil(t, window)
	require.Equal(t, 8, window.Lookback())
	b, ok, err := fx.data.Store().Bounds(startupSrc, "BTC")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at(2), b.Earliest, "新标的一次性拉取 N=8 根")
}

// 标的被移出集合：重新准备后窗口释放它们，变更跟进也不再处理它们的修正（设计 6.2，决定 18）。
func TestRemovedSubjectsLeaveWindowAndFollow(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.data.cfg.Lookback = func(seriesstore.Source) int { return 10 }
	fx.set.Set.SubjectMode = domain.SubjectModeInclude
	fx.set.Set.Subjects = []string{"A", "B"}
	fx.st.put("A", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	fx.st.put("B", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A", "B"}))
	window := fx.data.st(startupSrc).win.Load()
	require.Equal(t, []string{"A", "B"}, window.Subjects())

	fx.set.Set.Subjects = []string{"A"}
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A"}))
	require.Equal(t, []string{"A"}, window.Subjects(), "B 已移出集合，窗口释放它占的内存")

	// 修正 B 的历史：不再被跟进处理，本地的 B 保持原值；A 照常。
	fx.st.put("B", 900, 5)
	fx.st.put("A", 900, 5)
	_, err := fx.data.Follow(context.Background())
	require.NoError(t, err)
	got, err := fx.data.Store().ReadRange(context.Background(), startupSrc, []string{"A", "B"}, at(5), at(6))
	require.NoError(t, err)
	require.Equal(t, 905.0, got["A"].ValueAt(got["A"].FieldIndex("close"), 0).F)
	require.Equal(t, 105.0, got["B"].ValueAt(got["B"].FieldIndex("close"), 0).F)
}

func TestLongGapThreshold(t *testing.T) {
	require.Equal(t, 1441, LongGapThreshold(time.Minute), "1m：超过 24 小时")
	require.Equal(t, 25, LongGapThreshold(time.Hour))
	require.Equal(t, 3, LongGapThreshold(24*time.Hour), "日线按 3 期")
}

// 标的被移出集合期间 Storage 里的修正不会被跟进；重新加入时要先把本地保有的历史与 Storage 对账（审查意见 3）。
func TestReaddedSubjectIsResynced(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.data.cfg.Lookback = func(seriesstore.Source) int { return 10 }
	fx.set.Set.SubjectMode = domain.SubjectModeInclude
	fx.set.Set.Subjects = []string{"A", "B"}
	fx.st.put("A", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	fx.st.put("B", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A", "B"}))
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A"}))

	fx.st.put("B", 900, 5) // B 被移出期间发生的修正
	_, err := fx.data.Follow(context.Background())
	require.NoError(t, err) // 跟进过滤了 B，位点照常推进

	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A", "B"}))
	got, err := fx.data.Store().ReadRange(context.Background(), startupSrc, []string{"B"}, at(5), at(6))
	require.NoError(t, err)
	require.Equal(t, 905.0, got["B"].ValueAt(got["B"].FieldIndex("close"), 0).F, "重新加入时补上了被移出期间的修正")
}

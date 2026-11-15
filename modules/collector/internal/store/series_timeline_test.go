package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func timelineEntry(space, dataset string, p int64, subjects ...string) SeriesTimelineEntry {
	entry := SeriesTimelineEntry{SpaceID: space, DatasetID: dataset, Frequency: "1m", EffectiveFrom: p, LatenessS: 15}
	for _, subject := range subjects {
		entry.Series = append(entry.Series, seriesset.Series{SubjectID: subject})
	}
	if len(subjects) > 0 {
		entry.SeriesHash, entry.OutputFields = "hash", []string{"open", "close"}
	}
	return entry
}

// 按写入先后读出；同一生效点重复写入时后写的覆盖先写的并排到最后；空列表读成 nil；只读指定空间；关库重开后内容不变。
func TestSeriesTimelineListsInWriteOrderAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "collector.db")
	db, err := Open(&Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	plans := db.FetchPlans()

	record, err := plans.Create(ctx, "crypto", `{"v":1}`, "{}")
	require.NoError(t, err)
	require.NoError(t, plans.Update(ctx, record.ID, FetchPlanApplying, "{}", "",
		timelineEntry("crypto", "a", 100, "BTC-USDT", "ETH-USDT"),
		timelineEntry("crypto", "b", 100, "BTC-USDT"),
	))
	require.NoError(t, plans.Update(ctx, record.ID, FetchPlanApplying, "{}", "", timelineEntry("crypto", "a", 100, "BTC-USDT"), timelineEntry("crypto", "a", 200)))
	other, err := plans.Create(ctx, "stockcn", `{"v":1}`, "{}", timelineEntry("stockcn", "a", 100))
	require.NoError(t, err)
	require.NotZero(t, other.ID)
	want := []SeriesTimelineEntry{
		timelineEntry("crypto", "b", 100, "BTC-USDT"),
		timelineEntry("crypto", "a", 100, "BTC-USDT"),
		timelineEntry("crypto", "a", 200),
	}
	got, err := db.SeriesTimeline().ListBySpace(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Nil(t, got[2].Series, "停止行的空序列读成 nil")
	require.Nil(t, got[2].OutputFields)
	require.NoError(t, db.Close())

	reopened, err := Open(&Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	got, err = reopened.SeriesTimeline().ListBySpace(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, want, got, "重启后时间线不变")
}

// 时间线行与计划记录同一个事务：计划写不进去（已有未生效计划、计划已生效）时时间线行也不写；时间线行不合法时进度也不保存。
func TestSeriesTimelineIsWrittenWithThePlanRecord(t *testing.T) {
	ctx := context.Background()
	db := openPlanTestStore(t)
	plans, timeline := db.FetchPlans(), db.SeriesTimeline()

	open, err := plans.Create(ctx, "crypto", `{"v":1}`, "{}", timelineEntry("crypto", "gone", 100))
	require.NoError(t, err)
	// 一个空间只有一份未生效计划：新计划不能取代它，带来的停止行随事务回滚。
	_, err = plans.Create(ctx, "crypto", `{"v":2}`, "{}", timelineEntry("crypto", "other", 200))
	require.ErrorIs(t, err, ErrFetchPlanOpen)
	got, err := timeline.ListBySpace(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, []SeriesTimelineEntry{timelineEntry("crypto", "gone", 100)}, got)

	// 时间线行不合法：进度与行一起回滚。
	require.Error(t, plans.Update(ctx, open.ID, FetchPlanApplying, `{"registered":{"a|1m":true}}`, "", timelineEntry("crypto", "a", 0, "BTC-USDT")))
	record, err := plans.Open(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, FetchPlanPending, record.Status)
	require.Equal(t, "{}", record.Progress)

	require.NoError(t, plans.Update(ctx, open.ID, FetchPlanApplying, `{"registered":{"a|1m":true}}`, "", timelineEntry("crypto", "a", 100, "BTC-USDT")))
	require.NoError(t, plans.Update(ctx, open.ID, FetchPlanApplied, "{}", ""))
	// 已生效的计划不再更新，带来的行也不写。
	require.ErrorIs(t, plans.Update(ctx, open.ID, FetchPlanApplied, "{}", "", timelineEntry("crypto", "late", 300, "BTC-USDT")), gorm.ErrRecordNotFound)
	got, err = timeline.ListBySpace(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, []SeriesTimelineEntry{timelineEntry("crypto", "gone", 100), timelineEntry("crypto", "a", 100, "BTC-USDT")}, got)
}

// 重新登记改写已生效计划，并在同一个事务里删掉该空间的全部时间线行；目标不是已生效计划、或该空间有未生效的计划时什么都不动。
func TestReregisterAppliedPlanDeletesTheSpaceTimeline(t *testing.T) {
	ctx := context.Background()
	db := openPlanTestStore(t)
	plans, timeline := db.FetchPlans(), db.SeriesTimeline()
	applied, err := plans.Create(ctx, "crypto", `{"v":1}`, "{}")
	require.NoError(t, err)
	require.NoError(t, plans.Update(ctx, applied.ID, FetchPlanApplied, "{}", "", timelineEntry("crypto", "a", 100, "BTC-USDT"), timelineEntry("crypto", "retired", 50)))
	other, err := plans.Create(ctx, "stockcn", `{"v":1}`, "{}")
	require.NoError(t, err)
	require.NoError(t, plans.Update(ctx, other.ID, FetchPlanApplied, "{}", "", timelineEntry("stockcn", "a", 100, "600000.XSHG")))
	open, err := plans.Create(ctx, "crypto", `{"v":2}`, "{}")
	require.NoError(t, err)

	require.ErrorIs(t, plans.ReregisterAppliedPlan(ctx, open.ID, `{"v":3}`), gorm.ErrRecordNotFound, "未生效的计划不能这样改写")
	got, err := timeline.ListBySpace(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, got, 2, "拒绝时时间线不动")

	// 调用方在事务外查过“没有未生效计划”之后，对账器又建了新计划（open）：事务里复查到它，拒绝并保留时间线与计划内容。
	require.NoError(t, plans.Update(ctx, open.ID, FetchPlanApplying, "{}", "", timelineEntry("crypto", "a", 200, "BTC-USDT", "ETH-USDT")))
	require.ErrorIs(t, plans.ReregisterAppliedPlan(ctx, applied.ID, `{"v":"cleared"}`), ErrFetchPlanOpen)
	got, err = timeline.ListBySpace(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, got, 3, "新计划刚写的时间线行没有被删")
	history, err := plans.ListAll(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, `{"v":1}`, history[0].Plan, "拒绝时已生效计划不改写")

	require.NoError(t, plans.Update(ctx, open.ID, FetchPlanApplied, "{}", ""))
	require.NoError(t, plans.ReregisterAppliedPlan(ctx, applied.ID, `{"v":"cleared"}`))
	got, err = timeline.ListBySpace(ctx, "crypto")
	require.NoError(t, err)
	require.Empty(t, got, "该空间的时间线全部删掉，包括已退役的数据集")
	kept, err := timeline.ListBySpace(ctx, "stockcn")
	require.NoError(t, err)
	require.Len(t, kept, 1)
	history, err = plans.ListAll(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, `{"v":"cleared"}`, history[0].Plan)
}

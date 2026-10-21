package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

type memExclusions struct{ items []store.SubjectExclusion }

func (m *memExclusions) Active(_ context.Context, _ string, now time.Time) ([]store.SubjectExclusion, error) {
	var active []store.SubjectExclusion
	for _, item := range m.items {
		if item.ExpiresAt.IsZero() || item.ExpiresAt.After(now) {
			active = append(active, item)
		}
	}
	return active, nil
}

func finalSubjects(t *testing.T, content *FetchPlanContent) []string {
	t.Helper()
	var subjects []string
	for _, name := range content.SortedFunctionNames() {
		plan := content.FinalPlan(content.Functions[name])
		if plan == nil {
			continue
		}
		for _, dataset := range plan.Datasets {
			for _, subject := range dataset.Copies[len(dataset.Copies)-1].Subjects {
				subjects = append(subjects, subject.ID)
			}
		}
	}
	return subjects
}

// 排除后下一轮生成新计划且 hash 变化；到期自动恢复；只作用于匹配的市场。
func TestSubjectExclusionsChangeThePlanAndExpire(t *testing.T) {
	h := newIngestHarness(t, modePullSCF, "1m")
	exclusions := &memExclusions{}
	h.r.Exclusions = exclusions
	h.settle(true)
	before := h.applied().Datasets["ds_spot_1m|1m"].Hash
	require.Contains(t, finalSubjects(t, h.applied()), "S007-USDT")

	// 别的市场的排除不影响现货。
	exclusions.items = []store.SubjectExclusion{{SpaceID: "crypto", SubjectID: "S007-USDT", MarketType: "swap"}}
	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(h.r))
	require.Nil(t, h.open())

	exclusions.items = []store.SubjectExclusion{{SpaceID: "crypto", SubjectID: "S007-USDT", ExpiresAt: h.now.Add(time.Hour)}}
	require.NoError(t, h.reconcile(h.r))
	record := h.open()
	require.NotNil(t, record, "排除名单一变下一轮就生成新计划")
	change := h.content(record).Datasets["ds_spot_1m|1m"]
	require.Equal(t, []string{"S007-USDT"}, change.Removed)
	require.NotEqual(t, before, change.Hash)
	h.settle(true)
	require.NotContains(t, finalSubjects(t, h.applied()), "S007-USDT")
	require.Len(t, finalSubjects(t, h.applied()), 59)

	// 到期后标的自动回到计划。
	h.now = h.now.Add(time.Hour)
	h.settle(true)
	content := h.applied()
	require.Equal(t, before, content.Datasets["ds_spot_1m|1m"].Hash)
	require.Contains(t, finalSubjects(t, content), "S007-USDT")
}

// 交接进行中设置排除：已打开的交接计划先推进完，排除在下一份计划里生效。
func TestSubjectExclusionWaitsForAnOpenHandoff(t *testing.T) {
	h := newIngestHarness(t, modePullSCF, "1m")
	exclusions := &memExclusions{}
	h.r.Exclusions = exclusions
	h.settle(true)
	h.modes.set("ds_spot_1m", IngestStreaming, PlanExecutorLocal, false)
	require.NoError(t, h.reconcile(h.r))
	handoff := h.open()
	require.NotNil(t, handoff)
	require.Equal(t, PlanExecutorSCF, h.content(handoff).Datasets["ds_spot_1m|1m"].OldIngest)

	exclusions.items = []store.SubjectExclusion{{SpaceID: "crypto", SubjectID: "S001-USDT"}}
	h.now = h.now.Add(20 * time.Second)
	require.NoError(t, h.reconcile(h.r))
	require.Equal(t, handoff.ID, h.open().ID, "交接计划不被排除打断")
	require.Contains(t, finalSubjects(t, h.content(handoff)), "S001-USDT")

	last := h.settle(true)
	require.Equal(t, handoff.ID, last.ID)
	require.NoError(t, h.reconcile(h.r))
	next := h.open()
	require.NotNil(t, next)
	require.Equal(t, []string{"S001-USDT"}, h.content(next).Datasets["ds_spot_1m|1m"].Removed)
	h.settle(true)
	content := h.applied()
	require.Equal(t, PlanExecutorStreaming, content.Functions[streamItem].Executor)
	require.NotContains(t, finalSubjects(t, content), "S001-USDT")
}

func TestExcludeSubjectsDropsSymbolsPairsAndEmptyGroups(t *testing.T) {
	groups := []TaskGroup{
		{MarketType: "swap", DatasetID: "a", Subjects: []string{"BTC-USDT", "ETH-USDT"}, ExternalSymbols: map[string]string{"BTC-USDT": "BTCUSDT", "ETH-USDT": "ETHUSDT"}, Pairs: map[string]string{"BTC-USDT": "BTCUSD"}},
		{MarketType: "spot", DatasetID: "b", Subjects: []string{"BTC-USDT"}, ExternalSymbols: map[string]string{"BTC-USDT": "BTCUSDT"}},
	}
	got := excludeSubjects(groups, []store.SubjectExclusion{{SubjectID: "BTC-USDT"}})
	require.Len(t, got, 1)
	require.Equal(t, []string{"ETH-USDT"}, got[0].Subjects)
	require.Equal(t, map[string]string{"ETH-USDT": "ETHUSDT"}, got[0].ExternalSymbols)
	require.Nil(t, got[0].Pairs)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, groups[0].Subjects, "不改原分组")
}

// 全部标的都被排除是操作员的决定：不按“标的暂时取不到”保持现状，经过去抖后数据集退出计划；到期后作为新数据集回到计划。
func TestExcludingEverySubjectRetiresTheDatasetAndExpiryBringsItBack(t *testing.T) {
	h := newIngestHarness(t, modePullSCF, "1m")
	exclusions := &memExclusions{}
	h.r.Exclusions = exclusions
	h.settle(true)
	var all []store.SubjectExclusion
	for _, subject := range h.symbols.subjects {
		all = append(all, store.SubjectExclusion{SpaceID: "crypto", SubjectID: subject, ExpiresAt: h.now.Add(time.Hour)})
	}
	exclusions.items = all
	// 数据集整体消失要经过去抖（与任务暂时取不到分组同一规则）才退出计划。
	for i := 0; i < planGoneDebounce && h.open() == nil; i++ {
		require.NoError(t, h.reconcile(h.r))
		h.now = h.now.Add(20 * time.Second)
	}
	require.NotNil(t, h.open())
	h.settle(true)
	content := h.applied()
	require.True(t, content.Datasets["ds_spot_1m|1m"].Gone, "数据集退出计划")
	require.Empty(t, finalSubjects(t, content))

	h.now = h.now.Add(time.Hour)
	h.settle(true)
	content = h.applied()
	require.False(t, content.Datasets["ds_spot_1m|1m"].Gone)
	require.Len(t, finalSubjects(t, content), 60)
}

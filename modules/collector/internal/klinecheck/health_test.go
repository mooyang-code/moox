package klinecheck

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

func TestHealthReportsLagGapsAndHistory(t *testing.T) {
	backfiller, _, _, base := newBackfillFixture(t)
	ctx := context.Background()
	repo := backfiller.Store.(*store.BackfillRepository)
	now := backfiller.now()

	// 从未扫描过：滞后按最早生效点算，夹具里落后约 48 小时。
	health, err := Health(ctx, backfiller.Plans, repo, "crypto", now)
	require.NoError(t, err)
	require.Len(t, health, 1)
	require.Equal(t, "ds", health[0].DatasetID)
	require.Equal(t, 2, health[0].Subjects)
	require.True(t, health[0].EffectiveFrom.Equal(base))
	require.Equal(t, HealthError, health[0].Level)
	require.Contains(t, health[0].Reasons[0], "自动巡检已停")

	// 自动补采发现的实时缺口只展示，水位跟上后等级正常。
	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	require.NoError(t, repo.SetWatermark(ctx, "crypto", "ds", "1m", now.Add(-5*time.Minute)))
	health, err = Health(ctx, backfiller.Plans, repo, "crypto", now)
	require.NoError(t, err)
	require.Equal(t, HealthOK, health[0].Level, health[0].Reasons)
	require.Equal(t, 4, health[0].GapBars)
	require.Equal(t, 2, health[0].GapSubjects)

	// 未确认的历史缺口与扫描失败都需要关注。
	require.NoError(t, repo.SaveHistoryScan(ctx, store.HistoryScan{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", ScannedAt: now, GapBars: 6, GapSubjects: 2}))
	health, err = Health(ctx, backfiller.Plans, repo, "crypto", now)
	require.NoError(t, err)
	require.Equal(t, HealthWarn, health[0].Level)
	require.Equal(t, []string{"历史缺口 6 根（2 个标的）待补采确认"}, health[0].Reasons)

	// 已确认的行情源缺失只计数，不影响等级。
	require.NoError(t, repo.SaveHistoryScan(ctx, store.HistoryScan{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", ScannedAt: now}))
	require.NoError(t, repo.AddAbsences(ctx, []store.SourceAbsence{{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", SubjectID: "BTC-USDT", From: base, To: base.Add(2 * time.Minute), Bars: 2}}))
	health, err = Health(ctx, backfiller.Plans, repo, "crypto", now)
	require.NoError(t, err)
	require.Equal(t, HealthOK, health[0].Level)
	require.Equal(t, 2, health[0].AbsentBars)
}

func TestFormatDuration(t *testing.T) {
	require.Equal(t, "42 分钟", FormatDuration(42*time.Minute))
	require.Equal(t, "2 小时", FormatDuration(2*time.Hour))
	require.Equal(t, "1 小时 5 分钟", FormatDuration(65*time.Minute))
	require.Equal(t, "2 天", FormatDuration(50*time.Hour))
	require.Equal(t, HealthError, WorseLevel(HealthWarn, HealthError))
	require.Equal(t, HealthWarn, WorseLevel(HealthWarn, HealthOK))
}

// 低频数据集整点刚过、扫描槽还没到时，水位比“应扫到的时间”落后一个周期是正常的，不算滞后。
func TestHealthLagIsMeasuredFromWhenTheNextBarBecomesScannable(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	content := &marketfetch.FetchPlanContent{Datasets: map[string]*marketfetch.PlanDatasetChange{}, Functions: map[string]*marketfetch.PlanFunctionChange{}}
	change := &marketfetch.PlanDatasetChange{Changed: true, HashChanged: true, P: base.Unix()}
	change.DatasetID, change.Frequency, change.LatenessS = "ds", "1h", 60
	change.Series = append(change.Series, seriesOf("BTC-USDT"))
	content.Datasets["ds|1h"] = change
	plans := fakePlans{[]*store.FetchPlanRecord{{ID: 1, SpaceID: "crypto", Status: store.FetchPlanApplied, Plan: mustJSON(content), Progress: "{}"}}}
	repo := openBackfillStore(t)
	ctx := context.Background()
	watermark := base.Add(48 * time.Hour) // 已扫到 08:00（不含）
	require.NoError(t, repo.SetWatermark(ctx, "crypto", "ds", "1h", watermark))

	at := func(now time.Time) DatasetHealth {
		health, err := Health(ctx, plans, repo, "crypto", now)
		require.NoError(t, err)
		require.Len(t, health, 1)
		return health[0]
	}
	// 10:03：08:00 这根 K 线要到 10:01 才进入扫描范围，下一个扫描槽还没到，滞后 2 分钟，正常。
	early := at(watermark.Add(2*time.Hour + 3*time.Minute))
	require.Equal(t, 2*time.Minute, early.InspectLag)
	require.Equal(t, HealthOK, early.Level)
	// 10:31：已经超出预期 30 分钟，需关注。
	late := at(watermark.Add(2*time.Hour + 31*time.Minute))
	require.Equal(t, 30*time.Minute, late.InspectLag)
	require.Equal(t, HealthWarn, late.Level)
	// 水位停在非整点（追赶留下的）时，下一根从下一个整点算起。
	require.NoError(t, repo.SetWatermark(ctx, "crypto", "ds", "1h", watermark.Add(55*time.Minute)))
	require.Equal(t, time.Duration(0), at(watermark.Add(2*time.Hour+3*time.Minute)).InspectLag)
}

var marketdataMinute, _ = marketdata.ParseFrequency("1m")

// 1h 数据集刚到新的整点时，向前重叠的 5 分钟不应触发追赶：一次就扫到整点。
func TestAutoScanOverlapDoesNotTriggerCatchUp(t *testing.T) {
	backfiller, _, _, _ := newBackfillFixture(t)
	ctx := context.Background()
	repo := backfiller.Store.(*store.BackfillRepository)
	now := backfiller.now()
	due := alignDown(marketdataMinute, now.Add(-15*time.Second-time.Minute))
	// 水位落后正好一个窗口：from = 水位 - 重叠，to = due，二者相差窗口 + 重叠。
	require.NoError(t, repo.SetWatermark(ctx, "crypto", "ds", "1m", due.Add(-autoScanWindow)))
	require.NoError(t, backfiller.AutoScan(ctx, "crypto"))
	jobs, err := repo.ActiveJobs(ctx, "crypto")
	require.NoError(t, err)
	if len(jobs) > 0 {
		require.True(t, jobs[0].To.Equal(due), "一次扫到应扫到的时间，不留尾巴")
		return
	}
	mark, err := repo.Watermark(ctx, "crypto", "ds", "1m")
	require.NoError(t, err)
	require.True(t, mark.Equal(due), "没有缺口时水位直接推进到应扫到的时间")
}

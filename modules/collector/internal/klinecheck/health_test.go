package klinecheck

import (
	"context"
	"testing"
	"time"

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
	require.Contains(t, health[0].Reasons[0], "自动检查滞后")

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

package pipeline

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/stretchr/testify/require"
)

// 缺口恢复（只填充，按冻结的序列全集）：永续缺 SOL 时严格全集的截面因子只写现货；
// 项的结果按标的确认，有序列缺数或所在 tag 被跳过的标的都不算补齐，等永续补上后再恢复。
func TestUnifiedFillOnlyRecoveryConfirmsOnlyFullyWrittenSubjects(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	spread := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, false)
	subjects := []string{"BTC", "ETH", "SOL"}
	plan := unifiedPlan(start, []domain.FactorDef{spread}, subjects)
	plan.Mode, plan.FillOnly, plan.WriteCarry = ModeRecalc, true, false
	plan.TargetStart, plan.TargetEnd = start, start.Add(time.Minute)
	plan.UpstreamFailed, plan.UpstreamFailedSeries = nil, nil
	runner := NewRunner(&fakeStore{}, unifiedData(subjects, start, 2, series("SOL", tagSwap)), periodclock.Continuous{}, Config{})
	loaded, err := runner.Load(context.Background(), plan)
	require.NoError(t, err)
	computation, err := runner.Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	rows, report, err := runner.AssembleWithReport(plan, loaded, computation)
	require.NoError(t, err)

	require.Equal(t, "degraded", computation.FactorStates["spread"].Status)
	require.Len(t, rows, 3, "只写现货三条序列")
	for _, row := range rows {
		require.Equal(t, tagSpot, row.SeriesTag)
	}
	for _, subject := range subjects {
		require.Equal(t, ItemFailed, report.Items[ItemKey{Member: "spread", Subject: subject, Period: start}], subject)
	}

	// 永续补齐之后：全部写成，项都确认。
	full := NewRunner(&fakeStore{}, unifiedData(subjects, start, 2), periodclock.Continuous{}, Config{})
	loaded, err = full.Load(context.Background(), plan)
	require.NoError(t, err)
	computation, err = full.Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	rows, report, err = full.AssembleWithReport(plan, loaded, computation)
	require.NoError(t, err)
	require.Equal(t, "complete", computation.FactorStates["spread"].Status)
	require.Len(t, rows, 6)
	for _, subject := range subjects {
		require.Equal(t, ItemWritten, report.Items[ItemKey{Member: "spread", Subject: subject, Period: start}], subject)
	}
}

// 期望序列里同一条序列出现两次：按标的归组时去重，项照常确认（重复的 tag 曾让位图多要一位，这个标的的项永远确认不了）。
func TestDuplicateExpectedSeriesStillConfirms(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	spread := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, false)
	subjects := []string{"BTC", "ETH", "SOL"}
	plan := unifiedPlan(start, []domain.FactorDef{spread}, subjects)
	plan.Mode, plan.FillOnly, plan.WriteCarry = ModeRecalc, true, false
	plan.TargetStart, plan.TargetEnd = start, start.Add(time.Minute)
	plan.UpstreamFailed, plan.UpstreamFailedSeries = nil, nil
	plan.ExpectedSeries = append(plan.ExpectedSeries, series("BTC", tagSpot), series("BTC", tagSwap))
	require.Equal(t, map[string][]string{"BTC": {tagSpot, tagSwap}, "ETH": {tagSpot, tagSwap}, "SOL": {tagSpot, tagSwap}}, normalizeTags(plan.TagsBySubject()))
	runner := NewRunner(&fakeStore{}, unifiedData(subjects, start, 2), periodclock.Continuous{}, Config{})
	loaded, err := runner.Load(context.Background(), plan)
	require.NoError(t, err)
	computation, err := runner.Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	_, report, err := runner.AssembleWithReport(plan, loaded, computation)
	require.NoError(t, err)
	for _, subject := range subjects {
		require.Equal(t, ItemWritten, report.Items[ItemKey{Member: "spread", Subject: subject, Period: start}], subject)
	}
}

// normalizeTags 把每个标的的 tag 排序，便于比较。
func normalizeTags(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for subject, tags := range in {
		sorted := append([]string(nil), tags...)
		sort.Strings(sorted)
		out[subject] = sorted
	}
	return out
}

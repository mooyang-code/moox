package engine

import (
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
)

// K 线补写事件：同一个源数据集上的每个任务都记下这段周期已补齐、被唤醒追赶，并唤醒跳过恢复；
// 别的数据集或别的空间的事件不影响任何任务。
func TestOnBackfilledMarksPeriodsFilledAndKicksTasksOfTheDataset(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineTask{
		uncompiled(engineTask("ftask_a", "dataset_a", true, "bias")),
		uncompiled(engineTask("ftask_b", "dataset_b", true, "bias")),
	}})
	require.NoError(t, err)
	marks, err := watermark.Open(filepath.Join(t.TempDir(), "watermarks.json"))
	require.NoError(t, err)
	continuity, err := eventconsumer.NewContinuity(eventconsumer.ContinuityConfig{Store: marks, Local: struct{ eventconsumer.LocalRows }{}})
	require.NoError(t, err)
	gapKicks := 0
	r := &Runtime{catalog: cache, continuity: continuity, advanceKick: make(chan string, 4), filled: newFilledPeriods(time.Now), kickGap: func() { gapKicks++ }}

	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	payload := &eventpb.CollectorKlineBackfilled{
		DatasetId: "dataset_a", Frequency: "1m", SubjectIds: []string{"BTC-USDT"}, Origin: "auto",
		Start: start.Unix(), End: start.Add(10 * time.Minute).Unix(),
		Absent: []*eventpb.CollectorKlineAbsent{
			{SubjectId: "ETH-USDT", Start: start.Unix(), End: start.Add(2 * time.Minute).Unix()},
			{SubjectId: "BTC-USDT", Start: start.Unix(), End: start.Add(time.Minute).Unix()},
		},
	}
	r.onBackfilled("stockcn", payload)
	require.Empty(t, r.advanceKick, "别的空间的事件不唤醒任务")

	r.onBackfilled("crypto", payload)
	require.Equal(t, "ftask_a", <-r.advanceKick)
	require.Empty(t, r.advanceKick, "只唤醒源数据集相同的任务")
	require.True(t, r.filled.covers("ftask_a", start.Add(9*time.Minute)))
	require.False(t, r.filled.covers("ftask_a", start.Add(10*time.Minute)), "结束时间不含")
	require.False(t, r.filled.covers("ftask_b", start))
	require.Equal(t, 2, gapKicks)
}

// 补写事件把追赶目标推到补齐范围的最后一期，但不越过当前最后一根已收盘的根（整天重取的范围到次日零点）；
// 没有水位记录的任务不建立记录。
func TestOnBackfilledRaisesTheCatchupTargetUpToTheLatestClosedBar(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineTask{
		uncompiled(engineTask("ftask_a", "dataset_a", true, "bias")),
		uncompiled(engineTask("ftask_b", "dataset_a", true, "bias")),
	}})
	require.NoError(t, err)
	marks, err := watermark.Open(filepath.Join(t.TempDir(), "watermarks.json"))
	require.NoError(t, err)
	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	require.NoError(t, marks.Put(watermark.State{TaskID: "ftask_a", SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m", CommittedThrough: start.Add(-time.Minute), Target: start.Add(-time.Minute)}))
	continuity, err := eventconsumer.NewContinuity(eventconsumer.ContinuityConfig{Store: marks, Local: struct{ eventconsumer.LocalRows }{}})
	require.NoError(t, err)
	now := start.Add(30*time.Minute + 40*time.Second)
	r := &Runtime{catalog: cache, continuity: continuity, advanceKick: make(chan string, 4), filled: newFilledPeriods(func() time.Time { return now }), kickGap: func() {}}

	backfilled := func(from, to time.Time) {
		r.onBackfilled("crypto", &eventpb.CollectorKlineBackfilled{DatasetId: "dataset_a", Frequency: "1m", Origin: "auto",
			Start: from.Unix(), End: to.Unix()})
	}
	backfilled(start, start.Add(10*time.Minute))
	state, ok := marks.Get("ftask_a")
	require.True(t, ok)
	require.Equal(t, start.Add(9*time.Minute), state.Target, "目标推到补齐范围的最后一期")
	require.Equal(t, start.Add(-time.Minute), state.CommittedThrough, "只推目标，不动已提交的位置")

	backfilled(start.Add(-time.Hour), start.Add(-50*time.Minute))
	state, _ = marks.Get("ftask_a")
	require.Equal(t, start.Add(9*time.Minute), state.Target, "更早的补写不把目标往回拉")

	backfilled(start, start.Add(24*time.Hour))
	state, _ = marks.Get("ftask_a")
	require.Equal(t, start.Add(29*time.Minute), state.Target, "不越过当前最后一根已收盘、过了采集宽限的根（10:29 那根在 10:30 收盘，宽限 30 秒）")

	now = start.Add(31*time.Minute + 10*time.Second)
	backfilled(start, start.Add(24*time.Hour))
	state, _ = marks.Get("ftask_a")
	require.Equal(t, start.Add(29*time.Minute), state.Target, "10:30 那根 10:31 收盘，10:31:10 还在宽限内，不算")

	_, ok = marks.Get("ftask_b")
	require.False(t, ok, "没有水位记录的任务不建立记录")
}

package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
	"github.com/mooyang-code/moox/packages/storagepb"
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
	continuity, err := eventconsumer.NewContinuity(eventconsumer.ContinuityConfig{Store: marks, Reader: struct{ eventconsumer.PeriodReader }{}})
	require.NoError(t, err)
	gapKicks := 0
	r := &Runtime{catalog: cache, continuity: continuity, advanceKick: make(chan string, 4), filled: newFilledPeriods(time.Now), kickGap: func() { gapKicks++ }}

	start := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	payload := &storagepb.CollectorKlineBackfilled{
		DatasetId: "dataset_a", Frequency: "1m", SubjectIds: []string{"BTC-USDT"}, Origin: "auto",
		Start: timestamppb.New(start), End: timestamppb.New(start.Add(10 * time.Minute)),
		Absent: []*storagepb.CollectorKlineAbsent{
			{SubjectId: "ETH-USDT", Start: timestamppb.New(start), End: timestamppb.New(start.Add(2 * time.Minute))},
			{SubjectId: "BTC-USDT", SeriesTag: "swap", Start: timestamppb.New(start), End: timestamppb.New(start.Add(time.Minute))},
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

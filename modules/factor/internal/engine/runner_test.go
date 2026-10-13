package engine

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestStageSummaryListsStagesInPipelineOrder(t *testing.T) {
	summary := stageSummary(map[string]time.Duration{
		"write": 1500 * time.Millisecond, "load": 2 * time.Second, "compute": 250 * time.Microsecond,
	})

	require.Equal(t, "load=2s compute=0s write=1.5s", summary)
}

func TestWithWarmupMarksUnseenSetsWarming(t *testing.T) {
	lanes := withWarmup(
		[]domain.LaneStatus{{SetID: "a", Queued: 2, Active: true}},
		[]string{"a", "b"},
		[]pipeline.WarmupStatus{{SetID: "a", State: pipeline.WarmupReady, WarmSubjects: 5, ExpectedSubjects: 5}},
	)
	require.Equal(t, []domain.LaneStatus{
		{SetID: "a", Queued: 2, Active: true, WarmupState: pipeline.WarmupReady, WarmSubjects: 5, ExpectedSubjects: 5},
		{SetID: "b", WarmupState: pipeline.WarmupWarming},
	}, lanes)
}

type earliestStoreFake struct {
	storageio.Store
	at time.Time
}

func (s earliestStoreFake) EarliestPeriod(context.Context, string, string, string, []string, []string) (time.Time, bool, error) {
	return s.at, true, nil
}

// observedStore 嵌入的是 storageio.Store 接口，必须显式转发 EarliestPeriod，否则窗口空洞检查拿不到首根 K 线。
func TestObservedStoreForwardsEarliestPeriod(t *testing.T) {
	at := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	reader, ok := any(&observedStore{Store: earliestStoreFake{at: at}}).(earliestPeriodReader)
	require.True(t, ok)
	got, found, err := reader.EarliestPeriod(context.Background(), "crypto", "bars", "1m", []string{"BTC"}, []string{"close"})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, at, got)

	_, _, err = (&observedStore{Store: nil}).EarliestPeriod(context.Background(), "crypto", "bars", "1m", []string{"BTC"}, []string{"close"})
	require.Error(t, err, "底层不支持时返回错误（pipeline 据此保守判空洞）")
}

package eventconsumer

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	seriesSpot = "venue:binance|market:spot|source:spot_http"
	seriesSwap = "venue:binance|market:swap|source:swap_http"
)

// unifiedUniverse 造统一数据集的序列全集：每个标的有现货、永续两条序列。
func unifiedUniverse(subjects []string) []seriesset.Series {
	var out []seriesset.Series
	for _, subject := range subjects {
		out = append(out, seriesset.Series{SubjectID: subject, SeriesTag: seriesSpot}, seriesset.Series{SubjectID: subject, SeriesTag: seriesSwap})
	}
	return out
}

func tagSeries(subjects []string, tag string) []seriesset.Series {
	var out []seriesset.Series
	for _, subject := range subjects {
		out = append(out, seriesset.Series{SubjectID: subject, SeriesTag: tag})
	}
	return out
}

// seriesDelivery 是统一数据集的周期完成事件：带按序列的全集与失败名单。
func seriesDelivery(t *testing.T, eventID string, period time.Time, universe []string, failed []seriesset.Series) *jetstream.Delivery {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	status := "complete"
	var failedSubjects []string
	seen := map[string]bool{}
	var failedRefs []*storagepb.SeriesRef
	for _, item := range failed {
		status = "degraded"
		failedRefs = append(failedRefs, &storagepb.SeriesRef{SubjectId: item.SubjectID, SeriesTag: item.SeriesTag})
		if !seen[item.SubjectID] {
			seen[item.SubjectID] = true
			failedSubjects = append(failedSubjects, item.SubjectID)
		}
	}
	var universeRefs []*storagepb.SeriesRef
	for _, item := range unifiedUniverse(universe) {
		universeRefs = append(universeRefs, &storagepb.SeriesRef{SubjectId: item.SubjectID, SeriesTag: item.SeriesTag})
	}
	payload := &storagepb.DatasetPeriodCompleted{
		DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix(), Status: status, Source: "ledger", SnapshotRef: "scope-1",
		UniverseSubjectIds: universe, FailedSubjects: failedSubjects, UniverseSeries: universeRefs, FailedSeries: failedRefs,
		CompletedAt: timestamppb.New(period.Add(time.Minute)),
	}
	raw, err := registry.MarshalMessage(events.DatasetPeriodCompleted, payload, events.PublishOptions{EventID: eventID, OccurredAt: period.Add(time.Minute), SpaceID: "crypto", SubjectID: "bars"})
	require.NoError(t, err)
	subject, err := registry.RenderSubject(events.DatasetPeriodCompleted, "crypto", "bars")
	require.NoError(t, err)
	return &jetstream.Delivery{Subject: subject, RawData: raw, RawMessageID: eventID, ContentType: events.ContentType}
}

func (h *continuityHarness) seriesEvent(minute int, universe []string, failed []seriesset.Series) jetstream.HandlerResult {
	return h.handler.Handle(context.Background(), seriesDelivery(h.t, fmt.Sprintf("event-%d", minute), minuteAt(minute), universe, failed))
}

// seriesRows 是按序列读回的假 Storage：每期哪些（标的、series_tag）有行。
type seriesRows struct {
	mu      sync.Mutex
	present map[int64]map[seriesset.Series]bool
}

func (p *seriesRows) set(period time.Time, series ...seriesset.Series) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.present == nil {
		p.present = map[int64]map[seriesset.Series]bool{}
	}
	p.present[period.Unix()] = map[seriesset.Series]bool{}
	for _, item := range series {
		p.present[period.Unix()][item] = true
	}
}

func (p *seriesRows) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	frames := map[string]*storageio.Frame{}
	for _, subject := range req.Subjects {
		frame := &storageio.Frame{SubjectID: subject}
		for _, tag := range []string{seriesSpot, seriesSwap} {
			if p.present[req.Start.Unix()][seriesset.Series{SubjectID: subject, SeriesTag: tag}] {
				frame.Rows = append(frame.Rows, []any{req.Start, tag, 1.0})
			}
		}
		frames[subject] = frame
	}
	return frames, nil
}

// 永续整体晚到（一半的序列缺失，超过阈值）：现货在阈值以内，这一期有效，照常算，永续序列作为上游失败交给账本；
// 按标的计数的话整期会因全部标的都有失败而阻塞水位。
func TestContinuityUnifiedSwapEntirelyLateDoesNotBlockSpot(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.seriesEvent(0, universe, nil)
	swapLate := tagSeries(universe, seriesSwap)

	require.Equal(t, jetstream.ACK, h.seriesEvent(1, universe, swapLate).Decision)
	require.Len(t, h.runner.plans, 2)
	plan := h.runner.plans[1]
	require.Equal(t, minuteAt(1), plan.PeriodTime)
	require.Len(t, plan.ExpectedSeries, 40)
	require.ElementsMatch(t, swapLate, plan.UpstreamFailedSeries)
	require.Equal(t, minuteAt(1), h.state().CommittedThrough, "水位前进，没有阻塞")
	require.True(t, h.state().BlockedSince.IsZero())
	require.Len(t, h.state().UniverseSeries, 40, "水位状态记下按序列的全集，没有事件的周期读回用")
}

// 两个市场都缺（没有任何一个 tag 在阈值以内）：阻塞水位。
func TestContinuityUnifiedBlocksWhenEveryMarketIsOverTheThreshold(t *testing.T) {
	h := newContinuityHarness(t)
	universe := subjectsN(20)
	h.seriesEvent(0, universe, nil)
	both := unifiedUniverse(universe[:10])
	require.Equal(t, jetstream.ACK, h.seriesEvent(1, universe, both).Decision)
	require.Len(t, h.runner.plans, 1, "两个市场的缺失都超过阈值，不计算")
	require.Equal(t, "missing_over_threshold", h.state().BlockedReason)
}

// 没有事件的周期按序列读回：永续整体没到、现货齐全时，这一期照常算，只有永续序列记为失败。
func TestContinuityUnifiedReadbackJudgesSeriesNotSubjects(t *testing.T) {
	h := newContinuityHarness(t)
	rows := &seriesRows{}
	h.cont.cfg.Reader = rows
	universe := subjectsN(20)
	h.seriesEvent(0, universe, nil)
	rows.set(minuteAt(1), tagSeries(universe, seriesSpot)...)
	require.Equal(t, jetstream.ACK, h.seriesEvent(2, universe, nil).Decision)

	require.Equal(t, []time.Time{minuteAt(0), minuteAt(1), minuteAt(2)}, h.computed())
	catchup := h.runner.plans[1]
	require.Equal(t, CatchupEventID(h.taskID, minuteAt(1)), catchup.TriggerEventID)
	require.ElementsMatch(t, tagSeries(universe, seriesSwap), catchup.UpstreamFailedSeries, "读回里永续没有行")
	require.Empty(t, h.runner.plans[2].UpstreamFailedSeries)
	require.Equal(t, minuteAt(2), h.state().CommittedThrough)
}

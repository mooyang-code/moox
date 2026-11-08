package events

import (
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 统一数据集：按序列的全集与失败名单。失败序列必须是全集的子集，全集的标的必须正好是 universe_subject_ids。
func TestDatasetPeriodCompletedSeriesValidation(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	const spot, swap = "venue:binance|market:spot|source:spot_http", "venue:binance|market:swap|source:swap_http"
	build := func() *storagepb.DatasetPeriodCompleted {
		payload := validLedgerPeriodCompleted(timestamppb.Now())
		payload.Status = "degraded"
		payload.UniverseSubjectIds = []string{"BTC-USDT", "ETH-USDT"}
		payload.FailedSubjects = []string{"ETH-USDT"}
		payload.UniverseSeries = []*storagepb.SeriesRef{
			{SubjectId: "BTC-USDT", SeriesTag: spot}, {SubjectId: "BTC-USDT", SeriesTag: swap},
			{SubjectId: "ETH-USDT", SeriesTag: spot}, {SubjectId: "ETH-USDT", SeriesTag: swap},
		}
		payload.FailedSeries = []*storagepb.SeriesRef{{SubjectId: "ETH-USDT", SeriesTag: swap}}
		return payload
	}
	_, err = registry.Encode(DatasetPeriodCompleted, build(), validationOptions("event-1", "space", "dataset"))
	require.NoError(t, err)

	for name, mutate := range map[string]func(*storagepb.DatasetPeriodCompleted){
		"failed series not in universe": func(p *storagepb.DatasetPeriodCompleted) { p.FailedSeries[0].SeriesTag = "venue:okx" },
		"duplicate universe series":     func(p *storagepb.DatasetPeriodCompleted) { p.UniverseSeries[1] = p.UniverseSeries[0] },
		"universe subject missing":      func(p *storagepb.DatasetPeriodCompleted) { p.UniverseSeries = p.UniverseSeries[:2] },
		"universe subject not in subjects": func(p *storagepb.DatasetPeriodCompleted) {
			p.UniverseSubjectIds = []string{"BTC-USDT"}
			p.FailedSubjects = nil
		},
		"empty tag": func(p *storagepb.DatasetPeriodCompleted) { p.UniverseSeries[0].SeriesTag = "" },
		"failed series subject not in failed subjects": func(p *storagepb.DatasetPeriodCompleted) {
			p.FailedSeries = append(p.FailedSeries, &storagepb.SeriesRef{SubjectId: "BTC-USDT", SeriesTag: spot})
		},
		"tag over 128 bytes": func(p *storagepb.DatasetPeriodCompleted) {
			long := strings.Repeat("x", 129)
			p.UniverseSeries[3].SeriesTag, p.FailedSeries[0].SeriesTag = long, long
		},
		"tag with control character": func(p *storagepb.DatasetPeriodCompleted) {
			p.UniverseSeries[3].SeriesTag, p.FailedSeries[0].SeriesTag = "market:swap\n", "market:swap\n"
		},
		"failed series without universe": func(p *storagepb.DatasetPeriodCompleted) { p.UniverseSeries = nil },
		"complete has failed series": func(p *storagepb.DatasetPeriodCompleted) {
			p.Status = "complete"
			p.FailedSubjects = nil
		},
	} {
		broken := proto.Clone(build()).(*storagepb.DatasetPeriodCompleted)
		mutate(broken)
		_, err := registry.Encode(DatasetPeriodCompleted, broken, validationOptions("event-1", "space", "dataset"))
		require.Error(t, err, name)
	}
}

func TestCollectorKlineBackfilledSeriesTags(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	payload := &storagepb.CollectorKlineBackfilled{
		DatasetId: "dataset_src", Frequency: "5m", SubjectIds: []string{"BTC"},
		Start: timestamppb.New(start), End: timestamppb.New(start.Add(time.Hour)),
		Origin: "auto", JobId: "job-1", SyncRequestId: "batch-1", ConfirmedAt: timestamppb.New(start.Add(2 * time.Hour)),
		SeriesTags: []string{"venue:binance|market:swap|source:swap_http"},
	}
	options := PublishOptions{EventID: "batch-1", OccurredAt: start, SpaceID: "crypto", SubjectID: "dataset_src"}
	_, err = registry.Encode(CollectorKlineBackfilled, payload, options)
	require.NoError(t, err)
	payload.SeriesTags = []string{"a", "a"}
	_, err = registry.Encode(CollectorKlineBackfilled, payload, options)
	require.Error(t, err)
	payload.SeriesTags = []string{strings.Repeat("x", 129)}
	_, err = registry.Encode(CollectorKlineBackfilled, payload, options)
	require.Error(t, err, "series_tag 与行键同一套规则")
}

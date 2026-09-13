package engine

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestTaskContextPreservesParametersAndDefinitionType(t *testing.T) {
	start := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	task := &FactorTask{TaskID: "task", SubjectID: "BTC", Freq: "1m", StartTime: start,
		EndTime: start.Add(time.Minute), ConfigSnapshotID: "snap-2", SourceDataset: "mdataset_binance_kline_1m",
		Factor: FactorSpec{FactorID: "Bias", Name: "Bias", FactorType: domain.FactorTypeTimeSeries, ParamsJSON: `{"window":20}`}}
	meta, err := EncodeJSONRequestMeta(task, &DataFrame{})
	require.NoError(t, err)
	context := meta["context"].(map[string]any)
	require.Equal(t, "BTC", context["subject_id"])
	require.Equal(t, "1m", context["frequency"])
	require.Equal(t, start.Unix(), context["period_time"])
	require.Equal(t, "snap-2", context["config_snapshot_id"])
	require.Equal(t, "task", context["task_id"])
	require.NotContains(t, context, "input_contract_version")
	require.NotContains(t, context, "factor_type")
	require.Equal(t, "mdataset_binance_kline_1m", meta["source_dataset"])
	factor := meta["factor"].(map[string]any)
	require.Equal(t, "timeseries", factor["factor_type"])
	require.NotContains(t, context, "params")
}

func TestDatasetWindowContextUsesConfigSnapshot(t *testing.T) {
	start := time.Date(2026, 9, 13, 4, 1, 0, 0, time.UTC)
	meta, err := EncodeJSONRequestMeta(&FactorTask{
		TaskID: "window-task", SubjectID: "BTC-USDT", Freq: "1m", StartTime: start, EndTime: start.Add(time.Minute),
		ConfigSnapshotID: "mdataset-snap-1", SourceDataset: "mdataset_binance_kline_1m",
		Factor: FactorSpec{FactorID: "Bias", Name: "Bias", FactorType: domain.FactorTypeTimeSeries, ParamsJSON: `{}`},
	}, &DataFrame{})
	require.NoError(t, err)
	context := meta["context"].(map[string]any)
	require.Equal(t, "mdataset-snap-1", context["config_snapshot_id"])
	require.NotContains(t, context, "factor_type")
	require.NotContains(t, context, "input_contract_version")
}

func TestCrossSectionFrameCarriesSubjectIdentity(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	task := &FactorTask{TaskID: "rank", Freq: "1m", StartTime: at, EndTime: at.Add(time.Minute),
		ExpectedSubjects: []string{"BTC", "ETH"}, AvailableSubjects: []string{"BTC", "ETH"},
		Factor: FactorSpec{FactorID: "Rank", Name: "Rank", FactorType: domain.FactorTypeCrossSection, ParamsJSON: `{}`}}
	frame := &DataFrame{Columns: []string{"close"}, Rows: [][]any{{2}, {1}},
		DataTimes: []time.Time{at, at}, SeriesTags: []string{"", ""}, SubjectIDs: []string{"BTC", "ETH"}}
	meta, err := EncodeJSONRequestMeta(task, frame)
	require.NoError(t, err)
	require.Equal(t, []string{"data_time", "series_tag", "subject_id", "close"}, meta["df"].(map[string]any)["columns"])
	require.Equal(t, []string{"BTC", "ETH"}, meta["context"].(map[string]any)["expected_subjects"])
	frame.SubjectIDs = frame.SubjectIDs[:1]
	_, err = EncodeJSONRequestMeta(task, frame)
	require.ErrorContains(t, err, "subject")
}

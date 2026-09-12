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
		EndTime: start.Add(time.Minute), InputContractVersion: "view-contract-2",
		Factor: FactorSpec{FactorID: "Bias", Name: "Bias", FactorType: domain.FactorTypeTimeSeries, ParamsJSON: `{"window":20}`}}
	meta, err := EncodeJSONRequestMeta(task, &DataFrame{})
	require.NoError(t, err)
	context := meta["context"].(map[string]any)
	require.Equal(t, "BTC", context["subject_id"])
	require.Equal(t, "1m", context["frequency"])
	require.Equal(t, start.Unix(), context["period_time"])
	require.Equal(t, "view-contract-2", context["input_contract_version"])
	factor := meta["factor"].(map[string]any)
	require.Equal(t, "timeseries", factor["factor_type"])
	require.NotContains(t, context, "params")
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

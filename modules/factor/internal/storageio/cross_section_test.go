package storageio

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/stretchr/testify/require"
)

func TestCrossSectionRowsUseIndividualSubjects(t *testing.T) {
	task := &engine.FactorTask{SpaceID: "s", ResultDatasetID: "result", Freq: "1m",
		AvailableSubjects: []string{"BTC", "ETH"},
		Factor: engine.FactorSpec{FactorID: "rank", FactorType: "cross_section", Outputs: []string{"rank"}}}
	at := time.Now().UTC()
	result := &engine.FactorResult{Rows: []engine.FactorResultRow{
		{SubjectID: "BTC", DataTime: at, Values: map[string]any{"rank": 1.0}},
		{SubjectID: "ETH", DataTime: at, Values: map[string]any{"rank": 2.0}},
	}}
	rows, keys, err := buildFactorRows(task, result)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NotEqual(t, keys[0], keys[1])
	require.Contains(t, keys[0], "BTC")
	require.Contains(t, keys[1], "ETH")
	result.Rows[1].SubjectID = "OUTSIDE"
	_, _, err = buildFactorRows(task, result)
	require.ErrorContains(t, err, "outside")
	result.Rows[1].SubjectID = ""
	_, _, err = buildFactorRows(task, result)
	require.Error(t, err)
}

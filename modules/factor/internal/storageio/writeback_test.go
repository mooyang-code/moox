package storageio

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/stretchr/testify/require"
)

func TestWriteFactorPatchAllowsFactorToCreateNewSeriesTag(t *testing.T) {
	access := &fakeAccessClient{}
	client := &Client{access: access}
	at := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	written, err := client.WriteFactorPatch(context.Background(), &engine.FactorTask{
		TaskID: "spread-task", SpaceID: "crypto", TargetDataset: "spot_factor",
		SubjectID: "BTC-USDT", Freq: "1h",
		Factor: engine.FactorSpec{
			FactorID: "venue-spread", SourceHash: "source-hash", Outputs: []string{"spread"},
		},
	}, &engine.FactorResult{Rows: []engine.FactorResultRow{{
		DataTime: at, SeriesTag: "venue_pair:binance-okx",
		Values: map[string]any{"spread": 1.25},
	}}})
	require.NoError(t, err)
	require.EqualValues(t, 1, written)
	row := access.writeReqs[0].GetRows()[0]
	require.Equal(t, at.Format(time.RFC3339Nano), row.GetKey().GetTimeSeries().GetDataTime())
	require.Equal(t, "venue_pair:binance-okx", row.GetKey().GetTimeSeries().GetSeriesTag())
	require.Equal(t, "venue-spread", row.GetAttributes()["factor.id"].GetStringValue())
	require.Equal(t, "source-hash", row.GetAttributes()["factor.source_hash"].GetStringValue())
}

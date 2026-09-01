package cni

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

type recordingGetter struct {
	payload string
	query   url.Values
}

func (g *recordingGetter) Get(_ context.Context, _ string, _ string, query url.Values, result interface{}) error {
	g.query = query
	return json.Unmarshal([]byte(g.payload), result)
}

func TestClientFetchesAndNormalizesDailyIndexRows(t *testing.T) {
	getter := &recordingGetter{payload: `{"data":{"data":[
		["2026-08-28","0","3100","3000","2950","3050","0","1.2%","123456789","987654321","0"],
		["2026-08-31","0","3150","3050","3000","3100","0","1.6%","223456789","887654321","0"]
	]}}`}
	client := NewClient(getter)
	bars, err := client.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_cn", InstrumentType: "index", SubjectID: "SZ.399001", ProviderSymbol: "399001", Frequency: "1d",
		StartTime: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 8, 31, 15, 59, 0, 0, time.UTC), Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, bars, 1)
	require.Equal(t, "2026-08-30T16:00:00Z", bars[0].BarStart.Format(time.RFC3339))
	require.Equal(t, "3050", bars[0].Open.String())
	require.Equal(t, "223456789", bars[0].Amount.Value.String())
	require.Equal(t, "10k_share", bars[0].VolumeUnit)
	require.Equal(t, "100m_cny", bars[0].AmountUnit)
	require.Equal(t, "399001", getter.query.Get("indexCode"))
	require.Equal(t, "day", getter.query.Get("frequency"))
	require.Equal(t, "2026-08-29", getter.query.Get("startDate"))
	require.Equal(t, "2026-08-31", getter.query.Get("endDate"))
}

func TestParseRowsRejectsShortRow(t *testing.T) {
	_, err := parseRows([][]json.RawMessage{{json.RawMessage(`"2026-08-28"`)}}, marketdata.KlineRequest{}, time.FixedZone("CST", 8*60*60), time.Now())
	require.ErrorContains(t, err, "want at least 10")
}

package sw

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

func TestClientFetchesPeriodAndNormalizesDailyRows(t *testing.T) {
	getter := &recordingGetter{payload: `{"data":[
		{"swindexcode":"801001","bargaindate":"2026-08-28","openindex":"100","maxindex":"110","minindex":"95","closeindex":"105","bargainamount":"1234","bargainsum":"5678"},
		{"swindexcode":"801001","bargaindate":"2026-08-31","openindex":"105","maxindex":"115","minindex":"100","closeindex":"112","bargainamount":"2234","bargainsum":"6678"}
	]}`}
	client := NewClient(getter)
	bars, err := client.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_cn", InstrumentType: "index", SubjectID: "SW.801001", ProviderSymbol: "801001", Frequency: "1d", Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, bars, 2)
	require.Equal(t, "2026-08-27T16:00:00Z", bars[0].BarStart.Format(time.RFC3339))
	require.Equal(t, "100", bars[0].Open.String())
	require.Equal(t, "110", bars[0].High.String())
	require.Equal(t, "5678", bars[0].Amount.Value.String())
	require.Equal(t, "801001", getter.query.Get("swindexcode"))
	require.Equal(t, "DAY", getter.query.Get("period"))
}

func TestPeriodValueRejectsUnsupportedFrequency(t *testing.T) {
	_, err := periodValue("5m")
	require.ErrorContains(t, err, "unsupported frequency")
}

func TestParseRowsClampsMonthEndBar(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	rows := []map[string]json.RawMessage{{
		"bargaindate":   json.RawMessage(`"2026-01-31"`),
		"openindex":     json.RawMessage(`"100"`),
		"maxindex":      json.RawMessage(`"110"`),
		"minindex":      json.RawMessage(`"90"`),
		"closeindex":    json.RawMessage(`"105"`),
		"bargainamount": json.RawMessage(`"1000"`),
		"bargainsum":    json.RawMessage(`"100000"`),
	}}
	bars, err := parseRows(rows, marketdata.KlineRequest{SubjectID: "SW.801001", ProviderSymbol: "801001", Frequency: "1M"}, location, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, bars, 1)
	require.Equal(t, time.Date(2026, 2, 28, 0, 0, 0, 0, location).UTC(), bars[0].BarEnd)
}

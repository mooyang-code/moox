package sina

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestParseMinutePayload(t *testing.T) {
	rows, err := ParseMinutePayload([]byte(`var data=([{"day":"2026-08-31 09:31:00","open":"10","high":"10.5","low":"9.8","close":"10.2","volume":"100","amount":"1020"}]);`))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "2026-08-31 09:31:00", rows[0].Day)
	require.Equal(t, "10.2", rows[0].Close)
	require.Equal(t, "1020", rows[0].Amount)
}

func TestKlineSpecDoesNotClaimArbitraryHistoryRange(t *testing.T) {
	client := NewClient(nil)
	require.False(t, client.KlineSpec().SupportsRange)
	require.Equal(t, 1970, client.KlineSpec().MaxBarsPerRequest)
	require.Equal(t, "end-label", client.KlineSpec().TimestampMode)
}

func TestFetchKlinesUsesJSONPMinuteEndpointAndFiltersRange(t *testing.T) {
	getter := &recordingGetter{payload: []byte(`([{"day":"2026-08-31 09:35:00","open":"10","high":"10.5","low":"9.8","close":"10.2","volume":"100","amount":"1020"},{"day":"2026-08-31 09:40:00","open":"10.2","high":"10.6","low":"10.1","close":"10.5","volume":"200","amount":"2080"}]);`)}
	client := NewClient(getter)
	client.Now = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }
	bars, err := client.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_cn", InstrumentType: "equity", SubjectID: "SH.600000", ProviderSymbol: "SH.600000", Frequency: "5m",
		StartTime: time.Date(2026, 8, 31, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60)),
		EndTime:   time.Date(2026, 8, 31, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60)), Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, bars, 1)
	require.Equal(t, "10", bars[0].Open.String())
	require.Equal(t, "10.2", bars[0].Close.String())
	require.Equal(t, "2026-08-31T01:30:00Z", bars[0].BarStart.Format(time.RFC3339))
	require.Equal(t, "2026-08-31T01:35:00Z", bars[0].BarEnd.Format(time.RFC3339))
	require.Equal(t, "2026-08-31T01:35:00Z", bars[0].ProviderTime.UTC().Format(time.RFC3339))
	require.Equal(t, "share", bars[0].VolumeUnit)
	require.Equal(t, "cny", bars[0].AmountUnit)
	require.Equal(t, "sh600000", getter.query.Get("symbol"))
	require.Equal(t, "5", getter.query.Get("scale"))
	require.Equal(t, "no", getter.query.Get("ma"))
	require.Equal(t, "1970", getter.query.Get("datalen"))
	require.Equal(t, "quotes.sina.cn", getter.domain)
	require.Equal(t, "/cn/api/jsonp_v2.php/=/CN_MarketDataService.getKLineData", getter.path)
}

func TestParseMinutePayloadRejectsMissingAmount(t *testing.T) {
	_, err := ParseMinutePayload([]byte(`([{ "day":"2026-08-31 09:31:00", "open":"10", "high":"10.5", "low":"9.8", "close":"10.2", "volume":"100" }]);`))
	require.ErrorContains(t, err, "amount")
}

func TestParseMinutePayloadRejectsNonFiniteNumbers(t *testing.T) {
	for _, value := range []string{"NaN", "Inf", "-Inf"} {
		raw := []byte(`([{ "day":"2026-08-31 09:31:00", "open":"` + value + `", "high":"10.5", "low":"9.8", "close":"10.2", "volume":"100", "amount":"1020" }]);`)
		_, err := ParseMinutePayload(raw)
		require.ErrorContains(t, err, "finite")
	}
}

func TestNormalizeSymbolRequiresExplicitExchange(t *testing.T) {
	require.Equal(t, "sh600000", mustNormalizeSymbol("SH.600000"))
	_, err := normalizeSymbol("600000")
	require.Error(t, err)
}

type recordingGetter struct {
	payload []byte
	domain  string
	path    string
	query   url.Values
}

func (getter *recordingGetter) GetStream(_ context.Context, domain, path string, query url.Values, consume func(io.Reader) error) error {
	getter.domain = domain
	getter.path = path
	getter.query = query
	return consume(bytes.NewReader(getter.payload))
}

func mustNormalizeSymbol(value string) string {
	normalized, err := normalizeSymbol(value)
	if err != nil {
		panic(err)
	}
	return normalized
}

package ths

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

func TestParseYearPayload(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	bars, err := ParseYearPayload([]byte(`var data="20260828,10,11,9,10.5,100,12345,1.2;20260831,10.5,12,10,11.5,200,23456,1.2";`), location)
	require.NoError(t, err)
	require.Len(t, bars, 2)
	require.Equal(t, "2026-08-28T00:00:00+08:00", bars[0].Date.Format(time.RFC3339))
	open, err := bars[0].Open.Float64()
	require.NoError(t, err)
	require.Equal(t, 10.0, open)
	amount, err := bars[1].Amount.Float64()
	require.NoError(t, err)
	require.Equal(t, 23456.0, amount)
}

func TestParseYearPayloadRejectsNonDataPage(t *testing.T) {
	_, err := ParseYearPayload([]byte("<html>blocked</html>"), time.UTC)
	require.ErrorContains(t, err, "no quoted kline data")
}

func TestClientUsesAnnualEndpointAndFiltersRange(t *testing.T) {
	var requestedPath string
	getter := recordingGetter{payload: []byte(`var data="20260828,10,11,9,10.5,100,12345,1.2;20260831,10.5,12,10,11.5,200,23456,1.2";`), path: &requestedPath}
	client := NewClient(getter)
	client.Now = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }
	bars, err := client.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_cn", InstrumentType: "equity", SubjectID: "SZ.000001", ProviderSymbol: "SZ.000001", Frequency: "1d",
		StartTime: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 8, 31, 23, 0, 0, 0, time.UTC), Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, bars, 1)
	require.Equal(t, "/v2/line/hs_000001/00/2026.js", requestedPath)
	require.Equal(t, "2026-08-30T16:00:00Z", bars[0].BarStart.Format(time.RFC3339))
}

type recordingGetter struct {
	payload []byte
	path    *string
}

func (getter recordingGetter) GetStream(_ context.Context, _ string, path string, _ url.Values, consume func(io.Reader) error) error {
	*getter.path = path
	return consume(bytes.NewReader(getter.payload))
}

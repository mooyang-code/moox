package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestMarketDataAdapterFetchesClosedMinuteKline(t *testing.T) {
	fixturePath := filepath.Join("testdata", "marketdata_spot.json")
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v3/klines", r.URL.Path)
		body, err := os.ReadFile(fixturePath)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))

	now := time.Date(2026, 8, 29, 1, 32, 30, 0, time.UTC)
	adapter := NewMarketDataAdapter(AdapterConfig{
		ProductType: marketdata.ProductSpot,
		SpotBaseURL: "http://fixture.test",
		HTTPClient:  client,
		Now:         func() time.Time { return now },
	})

	rows, err := adapter.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID:      "BTC-USDT",
		ProviderSymbol: "BTC-USDT",
		Frequency:      "1m",
		Limit:          2,
		RequestID:      "req-binance",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "binance", rows[0].ProviderID)
	require.Equal(t, 1234.56, rows[0].AmountCNY)
	require.Equal(t, time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC), rows[0].BarStart)
	require.Equal(t, "BTC-USDT", rows[0].ProviderSymbol)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newFixtureClient(t *testing.T, handler http.Handler) *http.Client {
	t.Helper()
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			return recorder.Result(), nil
		}),
	}
}

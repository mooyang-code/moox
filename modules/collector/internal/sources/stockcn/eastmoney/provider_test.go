package eastmoney

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

func TestFetchKlinesParsesFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "kline.json")
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/qt/stock/kline/get", r.URL.Path)
		body, err := os.ReadFile(fixture)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID:      "600000.XSHG",
		ProviderSymbol: "sh600000",
		Frequency:      "1m",
		Limit:          2,
		RequestID:      "req-eastmoney",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1200.0, rows[0].VolumeShares)
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

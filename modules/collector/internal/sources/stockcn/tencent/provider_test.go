package tencent

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
	fixture := filepath.Join("testdata", "kline.js")
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/appstock/app/kline/mkline", r.URL.Path)
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
		RequestID:      "req-tencent",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC), rows[0].BarStart)
	require.Equal(t, 200.0, rows[0].VolumeShares)
	require.InDelta(t, 2040.0, rows[0].AmountCNY, 0.000001)
	require.True(t, rows[0].AmountEstimated)
}

func TestFetchKlinesRequestsFullPageForHistoricalCoverage(t *testing.T) {
	fixture := filepath.Join("testdata", "kline.js")
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "sh600000,m1,,320", r.URL.Query().Get("param"))
		body, err := os.ReadFile(fixture)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))
	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})

	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID: "600000.XSHG", ProviderSymbol: "sh600000", Frequency: "1m", Limit: 1,
		StartTime: now.Add(-time.Hour), EndTime: now, RequestID: "req-tencent-history",
	})

	require.NoError(t, err)
	require.NotEmpty(t, rows)
}

func TestKlineSpecDoesNotAdvertiseUnverifiedBSE(t *testing.T) {
	spec := (&Provider{}).KlineSpec()
	require.Equal(t, []string{"XSHG", "XSHE"}, spec.Exchanges)
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

package baidu

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

func TestFetchShadowKlinesAndRemainShadowOnly(t *testing.T) {
	fixture := filepath.Join("testdata", "shadow.json")
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/quotation_minute_ab", r.URL.Path)
		body, err := os.ReadFile(fixture)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))

	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC) }})
	spec := provider.ShadowSpec()
	require.False(t, spec.CompleteOHLCV)

	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(provider))
	_, err := registry.KlineFetcher("baidu")
	require.ErrorIs(t, err, marketdata.ErrFetcherNotSupported)

	points, err := provider.FetchShadowKlines(context.Background(), marketdata.KlineRequest{
		SubjectID:      "600000.XSHG",
		ProviderSymbol: "sh600000",
		Frequency:      "1m",
		Limit:          2,
		RequestID:      "req-baidu",
	})
	require.NoError(t, err)
	require.Len(t, points, 1)
	require.Equal(t, 10.2, points[0].Price)
}

func TestFetchInstrumentSnapshotPaginatesSuccessfullyAndCountsExchanges(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/getmarketrank", r.URL.Path)
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"Result": [
						{"code": "600000", "market": "1", "name": "Pudong Bank"},
						{"code": "000001", "market": "0", "name": "Ping An Bank"}
					]
				}
			}`))
		case "2":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"Result": [
						{"code": "920000", "market": "2", "name": "Beijing Stock"}
					]
				}
			}`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("pn"))
		}
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{
		MarketID:   "stock_cn",
		SnapshotAt: now,
		RequestID:  "req-baidu-instrument",
	})
	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	require.Equal(t, 2, snapshot.PageCount)
	require.Equal(t, map[string]int{"XBSE": 1, "XSHG": 1, "XSHE": 1}, snapshot.ExchangeCounts)
	require.Len(t, snapshot.Instruments, 3)
	require.Equal(t, "sh600000", snapshot.Instruments[0].ProviderSymbol)
	require.Equal(t, "sz000001", snapshot.Instruments[1].ProviderSymbol)
	require.Equal(t, "bj920000", snapshot.Instruments[2].ProviderSymbol)
}

func TestFetchInstrumentSnapshotFailsWhenLaterPageReturnsHTTPError(t *testing.T) {
	requests := make([]string, 0, 2)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Query().Get("pn"))
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"Result": [
						{"code": "600000", "market": "1", "name": "Pudong Bank"}
					]
				}
			}`))
		case "2":
			http.Error(w, "boom", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("pn"))
		}
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{
		MarketID:   "stock_cn",
		SnapshotAt: now,
		RequestID:  "req-baidu-instrument",
	})
	require.ErrorIs(t, err, marketdata.ErrHTTPStatus)
	require.Equal(t, []string{"1", "2"}, requests)
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

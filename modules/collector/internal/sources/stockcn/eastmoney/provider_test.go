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
		require.Equal(t, "1.600000", r.URL.Query().Get("secid"))
		require.Equal(t, "1", r.URL.Query().Get("klt"))
		require.Equal(t, "0", r.URL.Query().Get("fqt"))
		require.Equal(t, "20260827", r.URL.Query().Get("beg"))
		require.Equal(t, "20260829", r.URL.Query().Get("end"))
		require.Equal(t, "1205", r.URL.Query().Get("lmt"), "historical requests must fetch the provider page before common coverage filtering")
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
		StartTime:      time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC),
		RequestID:      "req-eastmoney",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1200.0, rows[0].VolumeShares)
}

func TestFetchInstrumentSnapshotPaginatesSuccessfullyAndCountsExchanges(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/qt/clist/get", r.URL.Path)
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"total": 3,
					"diff": [
						{"f12": "600000", "f13": 1, "f14": "Pudong Bank"},
						{"f12": "000001", "f13": 0, "f14": "Ping An Bank"}
					]
				}
			}`))
		case "2":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"total": 3,
					"diff": [
						{"f12": "920000", "f13": 2, "f14": "Beijing Stock"}
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
		MarketID:   "stockcn",
		SnapshotAt: now,
		RequestID:  "req-eastmoney-instrument",
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

func TestFetchInstrumentSnapshotDoesNotTreatShortPageAsCompleteWithoutTerminalEvidence(t *testing.T) {
	requests := make([]string, 0, 2)
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Query().Get("pn"))
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{"data":{"pagecount":2,"total":2,"diff":[{"f12":"600000","f13":1,"f14":"Pudong Bank"}]}}`))
		case "2":
			http.Error(w, "truncated", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("pn"))
		}
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, RateLimit: marketdata.RateLimitPolicy{RequestsPerSecond: 100, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second}})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "short-page"})

	require.ErrorIs(t, err, marketdata.ErrHTTPStatus)
	require.Equal(t, []string{"1", "2"}, requests)
}

func TestFetchInstrumentSnapshotRejectsDeclaredTotalMismatch(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("pn") {
		case "1":
			_, _ = w.Write([]byte(`{"data":{"pagecount":2,"total":3,"diff":[{"f12":"600000","f13":1,"f14":"Pudong Bank"}]}}`))
		case "2":
			_, _ = w.Write([]byte(`{"data":{"pagecount":2,"total":3,"diff":[{"f12":"000001","f13":0,"f14":"Ping An Bank"}]}}`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("pn"))
		}
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "declared-total"})

	require.ErrorContains(t, err, "does not match")
}

func TestFetchInstrumentSnapshotAcceptsObjectDiffWithDeclaredTotal(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"total":1,"diff":{"0":{"f12":"600000","f13":1,"f14":"Pudong Bank"}}}}`))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "object-diff"})

	require.NoError(t, err)
	require.Len(t, snapshot.Instruments, 1)
}

func TestFetchInstrumentSnapshotRejectsShortFinalPageWithoutCount(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pn") == "1" {
			_, _ = w.Write([]byte(`{"data":{"diff":[{"f12":"600000","f13":1,"f14":"Pudong Bank"}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"diff":[]}}`))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stockcn", RequestID: "short-empty"})

	require.ErrorContains(t, err, "unverified short page")
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
					"total": 2,
					"diff": [
						{"f12": "600000", "f13": 1, "f14": "Pudong Bank"}
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
		MarketID:   "stockcn",
		SnapshotAt: now,
		RequestID:  "req-eastmoney-instrument",
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

package sina

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestFetchKlinesParsesFixture(t *testing.T) {
	fixture := filepath.Join("testdata", "kline.json")
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/cn/api/jsonp_v2.php/var moox_kline=/CN_MarketDataService.getKLineData", r.URL.Path)
		body, err := os.ReadFile(fixture)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	rows, err := provider.FetchKlines(context.Background(), marketdata.KlineRequest{
		SubjectID:      "000001.XSHE",
		ProviderSymbol: "sz000001",
		Frequency:      "1m",
		Limit:          2,
		RequestID:      "req-sina",
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 300.0, rows[0].VolumeShares)
	require.Equal(t, time.Date(2026, 8, 29, 1, 29, 0, 0, time.UTC), rows[0].BarStart)
	require.Equal(t, time.Date(2026, 8, 29, 1, 30, 0, 0, time.UTC), rows[0].BarEnd)
	require.Equal(t, marketdata.TimestampModeClose, provider.KlineSpec().TimestampMode)
}

func TestFetchInstrumentSnapshotAcceptsEmptyTerminalPageAfterExactMultiple(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "2" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		items := make([]string, 100)
		for index := range items {
			items[index] = fmt.Sprintf(`{"symbol":"sh%06d","name":"stock"}`, 600000+index)
		}
		_, _ = fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stock_cn", SnapshotAt: now, RequestID: "req"})

	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	require.Equal(t, 2, snapshot.PageCount)
	require.Len(t, snapshot.Instruments, 100)
}

func TestFetchInstrumentSnapshotRejectsHTTP200ErrorObjectAsTerminalPage(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"error":"temporary"}`))
			return
		}
		items := make([]string, 100)
		for index := range items {
			items[index] = fmt.Sprintf(`{"symbol":"sh%06d","name":"stock"}`, 600000+index)
		}
		_, _ = fmt.Fprintf(w, "[%s]", strings.Join(items, ","))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stock_cn", RequestID: "req"})

	require.ErrorContains(t, err, "no recognized item list")
}

func TestFetchInstrumentSnapshotRejectsChangingDeclaredPageCount(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCount := 3
		if r.URL.Query().Get("page") == "2" {
			pageCount = 2
		}
		_, _ = fmt.Fprintf(w, `{"data":{"pagecount":%d,"list":[{"symbol":"sh60000%s","name":"stock"}]}}`, pageCount, r.URL.Query().Get("page"))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client})

	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stock_cn", RequestID: "req"})

	require.ErrorContains(t, err, "page count changed")
}

func TestFetchInstrumentSnapshotResetsGuardTimeoutForEveryPage(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		_, _ = fmt.Fprintf(w, `{"data":{"pagecount":2,"list":[{"symbol":"sh60000%s","name":"stock"}]}}`, r.URL.Query().Get("page"))
	}))
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, InstrumentRequestTimeout: 20 * time.Millisecond})

	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{MarketID: "stock_cn", RequestID: "req"})

	require.NoError(t, err)
	require.Equal(t, 2, snapshot.PageCount)
}

func TestFetchInstrumentSnapshotPaginatesSuccessfullyAndCountsExchanges(t *testing.T) {
	client := newFixtureClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/quotes_service/api/json_v2.php/Market_Center.getHQNodeData", r.URL.Path)
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"list": [
						{"symbol": "sh600000", "name": "Pudong Bank"},
						{"symbol": "sz000001", "name": "Ping An Bank"}
					]
				}
			}`))
		case "2":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"list": [
						{"symbol": "bj920000", "name": "Beijing Stock"}
					]
				}
			}`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	snapshot, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{
		MarketID:   "stock_cn",
		SnapshotAt: now,
		RequestID:  "req-sina-instrument",
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
		requests = append(requests, r.URL.Query().Get("page"))
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{
				"data": {
					"pagecount": 2,
					"list": [
						{"symbol": "sh600000", "name": "Pudong Bank"}
					]
				}
			}`))
		case "2":
			http.Error(w, "boom", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))

	now := time.Date(2026, 8, 29, 1, 32, 0, 0, time.UTC)
	provider := New(Config{BaseURL: "http://fixture.test", HTTPClient: client, Now: func() time.Time { return now }})
	_, err := provider.FetchInstrumentSnapshot(context.Background(), marketdata.InstrumentRequest{
		MarketID:   "stock_cn",
		SnapshotAt: now,
		RequestID:  "req-sina-instrument",
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

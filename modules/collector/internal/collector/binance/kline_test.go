package binance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	collectorpkg "github.com/mooyang-code/moox/modules/collector/internal/collector"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/exchange/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/mooyang-code/moox/modules/collector/pkg/storage"
)

func TestBuildKlineRowsUsesTimeSeriesKeyAndOnlyStorageColumns(t *testing.T) {
	t.Parallel()

	binding := StorageBinding{SpaceID: "crypto", KlineDatasetID: "binance_spot_kline"}
	openTime := time.Date(2026, 6, 25, 1, 2, 3, 4, time.FixedZone("CST", 8*60*60))
	rows, err := buildKlineRows([]*market.Kline{{
		Symbol:      "BTC-USDT",
		Interval:    "1m",
		OpenTime:    openTime,
		CloseTime:   openTime.Add(time.Minute),
		Open:        common.NewDecimal("100.25"),
		High:        common.NewDecimal("101.5"),
		Low:         common.NewDecimal("99.75"),
		Close:       common.NewDecimal("100.8"),
		Volume:      common.NewDecimal("12.34"),
		QuoteVolume: common.NewDecimal("1234.56"),
		TradeCount:  88,
	}}, "BTC-USDT", binding, "1m")
	if err != nil {
		t.Fatalf("buildKlineRows returned error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows length = %d, want 1", len(rows))
	}

	key := rows[0].Key
	if key.SpaceID != "crypto" || key.DatasetID != "binance_spot_kline" || key.SubjectID != "BTC-USDT" || key.Freq != "1m" {
		t.Fatalf("unexpected key: %+v", key)
	}
	if key.DataTime != openTime.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("data_time = %s, want %s", key.DataTime, openTime.UTC().Format(time.RFC3339Nano))
	}

	gotNames := make([]string, 0, len(rows[0].Columns))
	for _, col := range rows[0].Columns {
		gotNames = append(gotNames, col.ColumnName)
		if col.ColumnName == "candle_begin_time" || col.ColumnName == "candle_end_time" {
			t.Fatalf("unexpected legacy candle column: %s", col.ColumnName)
		}
	}
	wantNames := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}
	if len(gotNames) != len(wantNames) {
		t.Fatalf("column count = %d, want %d: %v", len(gotNames), len(wantNames), gotNames)
	}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Fatalf("column[%d] = %s, want %s", i, gotNames[i], wantNames[i])
		}
	}
}

func TestBuildKlineRowsSkipsUnclosedKlines(t *testing.T) {
	t.Parallel()

	now := time.Now()
	closedOpenTime := now.Add(-2 * time.Minute)
	unclosedOpenTime := now.Add(-time.Second)
	binding := StorageBinding{SpaceID: "crypto", KlineDatasetID: "binance_spot_kline"}

	rows, err := buildKlineRows([]*market.Kline{
		testKlineWithTimes("BTC-USDT", "1m", closedOpenTime, now.Add(-time.Second), "100.8"),
		testKlineWithTimes("BTC-USDT", "1m", unclosedOpenTime, now.Add(time.Minute), "101.8"),
	}, "BTC-USDT", binding, "1m")
	if err != nil {
		t.Fatalf("buildKlineRows returned error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows length = %d, want only the closed kline", len(rows))
	}
	if rows[0].Key.DataTime != closedOpenTime.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("data_time = %s, want closed kline time %s", rows[0].Key.DataTime, closedOpenTime.UTC().Format(time.RFC3339Nano))
	}
}

func TestBuildKlineRowsReturnsRetryableErrorWhenNoKlineClosed(t *testing.T) {
	t.Parallel()

	now := time.Now()
	binding := StorageBinding{SpaceID: "crypto", KlineDatasetID: "binance_spot_kline"}
	rows, err := buildKlineRows([]*market.Kline{
		testKlineWithTimes("BTC-USDT", "1m", now, now.Add(time.Minute), "100.8"),
	}, "BTC-USDT", binding, "1m")
	if err == nil {
		t.Fatalf("buildKlineRows returned nil error, want unclosed kline error")
	}
	if len(rows) != 0 {
		t.Fatalf("rows length = %d, want 0", len(rows))
	}
}

func TestFetchKlinesRetriesUntilKlineClosed(t *testing.T) {
	t.Parallel()

	var requests int32
	requestTimes := make(chan time.Time, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/klines" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		requestTimes <- time.Now()
		call := atomic.AddInt32(&requests, 1)
		closeTime := time.Now().Add(time.Hour)
		if call > 1 {
			closeTime = time.Now().Add(-time.Second)
		}
		openTime := closeTime.Add(-time.Minute)
		writeKlineResponse(t, w, openTime, closeTime)
	}))
	defer server.Close()

	client := binanceapi.NewClient()
	if err := client.SetSpotBaseURL(server.URL); err != nil {
		t.Fatalf("SetSpotBaseURL returned error: %v", err)
	}
	collector := &KlineCollector{
		client:  client,
		spotAPI: binanceapi.NewSpotAPI(client),
	}

	klines, err := collector.fetchKlines(context.Background(), &collectorpkg.CollectParams{
		InstType: InstTypeSPOT,
		Symbol:   "BTC-USDT",
		Interval: "1m",
	})
	if err != nil {
		t.Fatalf("fetchKlines returned error: %v", err)
	}
	if atomic.LoadInt32(&requests) < 2 {
		t.Fatalf("requests = %d, want retry after unclosed kline", requests)
	}
	firstRequestAt := <-requestTimes
	secondRequestAt := <-requestTimes
	if retryInterval := secondRequestAt.Sub(firstRequestAt); retryInterval < 150*time.Millisecond || retryInterval > 350*time.Millisecond {
		t.Fatalf("retry interval = %s, want around 200ms", retryInterval)
	}
	if len(klines) != 1 {
		t.Fatalf("klines length = %d, want 1", len(klines))
	}
	if !time.Now().After(klines[0].CloseTime) {
		t.Fatalf("returned kline close_time = %s, want closed", klines[0].CloseTime)
	}
}

func TestSendKlineRowsUsesConfiguredStorageAuth(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trpc.storage.access.Access/WriteTimeSeriesRows" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req storage.WriteTimeSeriesRowsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.AuthInfo.AppID != "collector-app" || req.AuthInfo.AppKey != "kline-key" {
			t.Fatalf("unexpected auth: %+v", req.AuthInfo)
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	defer server.Close()

	collector := &KlineCollector{}
	binding := StorageBinding{AuthInfo: StorageAuthInfo{AppID: "collector-app", AppKey: "kline-key"}}
	rows := []storage.TimeSeriesRow{{
		Key:     storage.TimeSeriesKey{SpaceID: "crypto", DatasetID: "binance_spot_kline", SubjectID: "BTC-USDT", Freq: "1m", DataTime: "2026-06-25T00:00:00Z"},
		Columns: []storage.ColumnValue{storage.DoubleField("open", 1)},
	}}
	if err := collector.sendTimeSeriesRowsWithRetry(context.Background(), server.URL, binding, rows); err != nil {
		t.Fatalf("sendTimeSeriesRowsWithRetry returned error: %v", err)
	}
}

func testKlineWithTimes(symbol, interval string, openTime, closeTime time.Time, close string) *market.Kline {
	return &market.Kline{
		Symbol:      symbol,
		Interval:    interval,
		OpenTime:    openTime,
		CloseTime:   closeTime,
		Open:        common.NewDecimal("100.25"),
		High:        common.NewDecimal("101.5"),
		Low:         common.NewDecimal("99.75"),
		Close:       common.NewDecimal(close),
		Volume:      common.NewDecimal("12.34"),
		QuoteVolume: common.NewDecimal("1234.56"),
		TradeCount:  88,
	}
}

func writeKlineResponse(t *testing.T, w http.ResponseWriter, openTime time.Time, closeTime time.Time) {
	t.Helper()
	payload := [][]interface{}{{
		openTime.UnixMilli(),
		"100.25",
		"101.5",
		"99.75",
		"100.8",
		"12.34",
		closeTime.UnixMilli(),
		"1234.56",
		int64(88),
	}}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

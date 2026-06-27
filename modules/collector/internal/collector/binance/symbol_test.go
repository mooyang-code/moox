package binance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/exchange"
	"github.com/mooyang-code/moox/modules/collector/pkg/storage"
)

func TestBuildSymbolRegisterRequestBindsConfiguredDatasets(t *testing.T) {
	t.Parallel()

	binding := StorageBinding{
		SpaceID:         "crypto",
		DataSourceID:    "binance",
		SubjectType:     "crypto_pair",
		SubjectMarket:   "spot",
		RecordDatasetID: "binance_spot_symbols",
		KlineDatasetID:  "binance_spot_kline",
		BindDatasetIDs:  []string{"binance_spot_symbols", "binance_spot_kline"},
	}
	symbol := &exchange.SymbolInfo{Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active"}

	req := buildSymbolRegisterRequest(symbol, binding)
	if req.SpaceID != "crypto" || req.DataSourceID != "binance" || req.ExternalSymbol != "BTCUSDT" {
		t.Fatalf("unexpected register envelope: %+v", req)
	}
	if req.Subject.SubjectID != "BTC-USDT" || req.Subject.SubjectType != "crypto_pair" || req.Subject.Market != "spot" || req.Subject.Name != "BTC-USDT" || req.Subject.Status != "active" {
		t.Fatalf("unexpected subject: %+v", req.Subject)
	}
	if len(req.DatasetBindings) != 2 {
		t.Fatalf("bindings length = %d, want 2", len(req.DatasetBindings))
	}
	for i, wantID := range []string{"binance_spot_symbols", "binance_spot_kline"} {
		if req.DatasetBindings[i].SpaceID != "crypto" || req.DatasetBindings[i].DatasetID != wantID || req.DatasetBindings[i].SubjectID != "BTC-USDT" || req.DatasetBindings[i].Status != "active" {
			t.Fatalf("unexpected binding[%d]: %+v", i, req.DatasetBindings[i])
		}
	}
}

func TestBuildSymbolRecordRowsUsesConfiguredDatasetAndStorageColumns(t *testing.T) {
	t.Parallel()

	binding := StorageBinding{
		SpaceID:         "crypto",
		RecordDatasetID: "binance_swap_symbols",
	}
	rows, err := buildSymbolRecordRows([]*exchange.SymbolInfo{{
		Symbol:     "ETHUSDT",
		BaseAsset:  "ETH",
		QuoteAsset: "USDT",
		Status:     "active",
		MinQty:     "0.001",
		MaxQty:     "1000",
		TickSize:   "0.01",
		LotSize:    "0.001",
	}}, binding)
	if err != nil {
		t.Fatalf("buildSymbolRecordRows returned error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows length = %d, want 1", len(rows))
	}
	key := rows[0].Key
	if key.SpaceID != "crypto" || key.DatasetID != "binance_swap_symbols" || key.RecordID != "ETH-USDT" || key.Version != "latest" {
		t.Fatalf("unexpected record key: %+v", key)
	}

	wantColumns := map[string]string{
		"symbol":          "ETH-USDT",
		"external_symbol": "ETHUSDT",
		"base_asset":      "ETH",
		"quote_asset":     "USDT",
		"status":          "active",
	}
	wantDoubleColumns := map[string]float64{
		"min_qty":   0.001,
		"max_qty":   1000,
		"tick_size": 0.01,
		"lot_size":  0.001,
	}
	if len(rows[0].Columns) != len(wantColumns)+len(wantDoubleColumns) {
		t.Fatalf("column count = %d, want %d: %+v", len(rows[0].Columns), len(wantColumns)+len(wantDoubleColumns), rows[0].Columns)
	}
	for _, col := range rows[0].Columns {
		if want, ok := wantColumns[col.ColumnName]; ok {
			if col.ValueType != "FIELD_VALUE_TYPE_STRING" || col.Value.StringValue == nil || *col.Value.StringValue != want {
				t.Fatalf("column %s value = %+v, want string %s", col.ColumnName, col.Value, want)
			}
			delete(wantColumns, col.ColumnName)
			continue
		}
		if want, ok := wantDoubleColumns[col.ColumnName]; ok {
			if col.ValueType != "FIELD_VALUE_TYPE_DOUBLE" || col.Value.DoubleValue == nil || *col.Value.DoubleValue != want {
				t.Fatalf("column %s value = %+v, want double %v", col.ColumnName, col.Value, want)
			}
			delete(wantDoubleColumns, col.ColumnName)
			continue
		}
		t.Fatalf("unexpected column: %s", col.ColumnName)
	}
	if len(wantColumns) != 0 || len(wantDoubleColumns) != 0 {
		t.Fatalf("missing columns: strings=%+v doubles=%+v", wantColumns, wantDoubleColumns)
	}
}

func TestBuildSymbolRecordRowsOmitEmptyNumericColumnsAndRejectInvalidValues(t *testing.T) {
	t.Parallel()

	binding := StorageBinding{SpaceID: "crypto", RecordDatasetID: "binance_spot_symbols"}
	rows, err := buildSymbolRecordRows([]*exchange.SymbolInfo{{
		Symbol:     "BTCUSDT",
		BaseAsset:  "BTC",
		QuoteAsset: "USDT",
		Status:     "active",
	}}, binding)
	if err != nil {
		t.Fatalf("buildSymbolRecordRows with empty numeric values returned error: %v", err)
	}
	for _, col := range rows[0].Columns {
		switch col.ColumnName {
		case "min_qty", "max_qty", "tick_size", "lot_size":
			t.Fatalf("empty numeric column should be omitted: %+v", col)
		}
	}

	_, err = buildSymbolRecordRows([]*exchange.SymbolInfo{{
		Symbol:     "BTCUSDT",
		BaseAsset:  "BTC",
		QuoteAsset: "USDT",
		Status:     "active",
		MinQty:     "not-a-number",
	}}, binding)
	if err == nil {
		t.Fatalf("expected invalid numeric field error")
	}
}

func TestSendSymbolBatchWritesRecordsBeforeRegisteringSubjects(t *testing.T) {
	t.Parallel()

	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/trpc.moox.storage.Access/WriteRecordRows":
			var req storage.WriteRecordRowsRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode write request: %v", err)
			}
			if req.AuthInfo.AppID != "collector-app" || req.AuthInfo.AppKey != "collector-key" {
				t.Fatalf("unexpected write auth: %+v", req.AuthInfo)
			}
		case "/trpc.moox.storage.Metadata/RegisterDataSubject":
			var req storage.RegisterDataSubjectRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode register request: %v", err)
			}
			if req.AuthInfo.AppID != "collector-app" || req.AuthInfo.AppKey != "collector-key" {
				t.Fatalf("unexpected register auth: %+v", req.AuthInfo)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
	}))
	defer server.Close()

	binding := StorageBinding{
		SpaceID:         "crypto",
		DataSourceID:    "binance",
		SubjectType:     "crypto_pair",
		SubjectMarket:   "spot",
		RecordDatasetID: "binance_spot_symbols",
		BindDatasetIDs:  []string{"binance_spot_symbols"},
		AuthInfo:        StorageAuthInfo{AppID: "collector-app", AppKey: "collector-key"},
	}
	symbols := []*exchange.SymbolInfo{{
		Symbol:     "BTCUSDT",
		BaseAsset:  "BTC",
		QuoteAsset: "USDT",
		Status:     "active",
	}}
	rows, err := buildSymbolRecordRows(symbols, binding)
	if err != nil {
		t.Fatalf("buildSymbolRecordRows returned error: %v", err)
	}

	collector := &SymbolCollector{}
	if err := collector.sendSymbolBatchWithRetry(context.Background(), server.URL, binding, symbols, rows, 0, 1); err != nil {
		t.Fatalf("sendSymbolBatchWithRetry returned error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want write then register", calls)
	}
	if calls[0] != "/trpc.moox.storage.Access/WriteRecordRows" ||
		calls[1] != "/trpc.moox.storage.Metadata/RegisterDataSubject" {
		t.Fatalf("calls = %v, want write before register", calls)
	}
}

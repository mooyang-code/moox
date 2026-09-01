package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	tdxwire "github.com/mooyang-code/moox/packages/tdx"
)

type testWriter struct {
	mu   sync.Mutex
	rows int
}

type sourceTestWriter struct {
	testWriter
	sources     []string
	payloadRows []*storagepb.RowFieldUpsert
}

func (writer *testWriter) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.rows += len(rows)
	return nil
}

func (writer *testWriter) RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error {
	return nil
}

func (writer *sourceTestWriter) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, source string) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.rows += len(rows)
	writer.sources = append(writer.sources, source)
	writer.payloadRows = append(writer.payloadRows, rows...)
	return nil
}

type testGetter struct{}

func (testGetter) Get(_ context.Context, _ string, _ string, _ url.Values, result interface{}) error {
	return json.Unmarshal([]byte(`{"rc":0,"data":{"klines":["2026-08-31 09:30,10,10.5,11,9.5,100,1050"]}}`), result)
}

func (testGetter) GetStream(_ context.Context, _ string, _ string, _ url.Values, consume func(io.Reader) error) error {
	return consume(strings.NewReader(`{"data":{}}`))
}

type tencentTestGetter struct{}

func (tencentTestGetter) Get(context.Context, string, string, url.Values, interface{}) error {
	return nil
}

func (tencentTestGetter) GetStream(_ context.Context, _ string, _ string, _ url.Values, consume func(io.Reader) error) error {
	return consume(strings.NewReader(`v_kline_day2026={"code":0,"data":{"sz000001":{"day":[["2026-08-28","10","10.5","11","9.5","100","0","1.5","123.45"]]}}};`))
}

func TestHandlerRunsGenericHTTPPipeline(t *testing.T) {
	writer := &sourceTestWriter{}
	handler := &Handler{
		NewStorage: func(string, string, string) (marketfetch.KlineRowWriter, error) { return writer, nil },
		NewGetter:  func() markethttp.Getter { return testGetter{} },
		Now:        func() time.Time { return time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC) },
	}
	raw, err := json.Marshal(map[string]interface{}{
		"action":                     "market_fetch",
		"storage_rpc_gateway_target": "ip://127.0.0.1:11003",
		"data": map[string]interface{}{
			"space_id": "stock_cn", "dataset_id": "stock_cn_kline", "market_id": "stock_cn", "instrument_type": "equity", "frequency": "1m", "source_event_id": "e2e", "items": []map[string]string{{"subject_id": "SH.600000", "provider_symbol": "SH.600000"}, {"subject_id": "SH.600001", "provider_symbol": "SH.600001"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := handler.HandleRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got := response.(Response)
	if !got.Success || got.RowsWritten != 2 || writer.rows != 2 || len(writer.sources) != 2 || writer.sources[0] == writer.sources[1] {
		t.Fatalf("unexpected response=%+v rows=%d", got, writer.rows)
	}
}

func TestHandlerRunsEgressProbeAction(t *testing.T) {
	handler := &Handler{
		NewStorage: func(string, string, string) (marketfetch.KlineRowWriter, error) { return &testWriter{}, nil },
		NewGetter:  func() markethttp.Getter { return testGetter{} },
		ProbeEgress: func(_ context.Context, provider, market string) (*model.Response, error) {
			if provider != "binance" || market != "spot" {
				t.Fatalf("unexpected probe args provider=%q market=%q", provider, market)
			}
			return &model.Response{Success: true, Message: "egress probe ok", Data: map[string]interface{}{"details": map[string]string{"public_ip": "203.0.113.10"}}}, nil
		},
	}
	response, err := handler.HandleRequest(context.Background(), []byte(`{"action":"egress_probe","data":{"provider":"binance","market_type":"spot"}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := response.(*model.Response)
	if !ok || got == nil || !got.Success {
		t.Fatalf("unexpected response=%T %#v", response, response)
	}
	data, ok := got.Data.(map[string]interface{})
	if !ok || data["details"] == nil {
		t.Fatalf("egress probe data missing: %#v", got.Data)
	}
}

func TestHandlerHonorsZeroSettleDelay(t *testing.T) {
	t.Setenv("MOOX_MARKET_FETCH_SETTLE_DELAY_SECONDS", "0")
	writer := &sourceTestWriter{}
	handler := &Handler{
		NewStorage: func(string, string, string) (marketfetch.KlineRowWriter, error) { return writer, nil },
		NewGetter:  func() markethttp.Getter { return testGetter{} },
		Now:        func() time.Time { return time.Date(2026, 8, 31, 1, 31, 0, 0, time.UTC) },
	}
	raw, err := json.Marshal(map[string]interface{}{
		"action":                     "market_fetch",
		"storage_rpc_gateway_target": "ip://127.0.0.1:11003",
		"data": map[string]interface{}{
			"space_id": "stock_cn", "dataset_id": "stock_cn_kline", "market_id": "stock_cn", "instrument_type": "equity", "frequency": "1m", "source_event_id": "zero-settle", "items": []map[string]string{{"subject_id": "SH.600000", "provider_symbol": "SH.600000"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := handler.HandleRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got := response.(Response)
	if !got.Success || got.RowsWritten != 1 || writer.rows != 1 {
		t.Fatalf("zero settle delay response=%+v rows=%d", got, writer.rows)
	}
}

func TestHandlerRunsTencentJSONPThroughGenericPipeline(t *testing.T) {
	writer := &sourceTestWriter{}
	handler := &Handler{
		NewStorage: func(string, string, string) (marketfetch.KlineRowWriter, error) { return writer, nil },
		NewGetter:  func() markethttp.Getter { return tencentTestGetter{} },
		Now:        func() time.Time { return time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC) },
	}
	raw, err := json.Marshal(map[string]interface{}{
		"action":                     "market_fetch",
		"storage_rpc_gateway_target": "ip://127.0.0.1:11003",
		"data": map[string]interface{}{
			"space_id": "stock_cn", "dataset_id": "stock_cn_kline", "market_id": "stock_cn", "instrument_type": "equity",
			"provider_id": "tencent", "source_id": "stock_cn_http", "frequency": "1d", "source_event_id": "tencent-e2e",
			"start_time": "2026-08-01T00:00:00Z", "end_time": "2026-08-31T00:00:00Z", "items": []map[string]string{{"subject_id": "SZ.000001", "provider_symbol": "SZ.000001"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := handler.HandleRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got := response.(Response)
	if !got.Success || got.RowsWritten != 1 || writer.rows != 1 || len(writer.sources) != 1 {
		t.Fatalf("unexpected Tencent response=%+v rows=%d sources=%v", got, writer.rows, writer.sources)
	}
	writer.mu.Lock()
	row := writer.payloadRows[0]
	writer.mu.Unlock()
	if row.GetKey().GetTimeSeries().GetDataTime() != "2026-08-27T16:00:00Z" {
		t.Fatalf("unexpected Tencent data_time=%q", row.GetKey().GetTimeSeries().GetDataTime())
	}
	if got := row.GetFields()[4].GetValue().GetDoubleValue(); got != 10000 {
		t.Fatalf("unexpected Tencent volume=%v", got)
	}
	if got := row.GetFields()[5].GetValue().GetDoubleValue(); got != 1234500 {
		t.Fatalf("unexpected Tencent amount=%v", got)
	}
}

func TestHandlerRoutesSymbolSnapshotThroughGenericEntrypoint(t *testing.T) {
	writer := &testWriter{}
	called := false
	completionPublished := false
	handler := &Handler{
		NewStorage: func(string, string, string) (marketfetch.KlineRowWriter, error) { return writer, nil },
		NewGetter:  func() markethttp.Getter { return testGetter{} },
		PublishCompletion: func(_ context.Context, request Request, requestID string, results []itemResult) error {
			completionPublished = true
			if request.BatchKind != "symbol_snapshot" || requestID != "symbol-event" || len(results) != 1 || results[0].err != nil {
				t.Fatalf("unexpected symbol completion input: request=%+v request_id=%q results=%+v", request, requestID, results)
			}
			return nil
		},
		RunSymbolSnapshot: func(_ context.Context, request Request, storage marketfetch.Storage, _ *markets.Composition, requestID string) (*model.Response, error) {
			called = true
			if request.DataType != "symbol" || request.InstrumentType != "spot" || request.Items[0].ProviderSymbol != "" {
				t.Fatalf("unexpected symbol request: %+v", request)
			}
			if storage == nil || requestID != "symbol-event" {
				t.Fatalf("symbol snapshot dependencies not forwarded: storage=%v request_id=%q", storage, requestID)
			}
			return &model.Response{Success: true, Message: "succeeded", RequestID: requestID}, nil
		},
		Now: time.Now,
	}
	raw, err := json.Marshal(map[string]interface{}{
		"action":                     "market_fetch",
		"request_id":                 "symbol-event",
		"storage_rpc_gateway_target": "ip://127.0.0.1:11003",
		"data": map[string]interface{}{
			"space_id": "crypto", "dataset_id": "binance_spot_symbols", "market_id": "crypto", "instrument_type": "spot",
			"provider_id": "binance", "source_id": "spot_http", "data_type": "symbol", "batch_kind": "symbol_snapshot", "schedule_id": "symbol-schedule", "frequency": "1h", "source_event_id": "symbol-event", "node_id": "node-symbols",
			"snapshot_shard_index": 2, "snapshot_shard_count": 32,
			"items": []map[string]string{{"subject_id": "binance_spot_symbols"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := handler.HandleRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got := response.(Response)
	if !called || !completionPublished || !got.Success || got.RequestID != "symbol-event" {
		t.Fatalf("unexpected symbol response=%+v called=%v completion=%v", got, called, completionPublished)
	}
}

func TestHandlerPublishesFailedSymbolSnapshotCompletion(t *testing.T) {
	writer := &sourceTestWriter{}
	var completionErr error
	handler := &Handler{
		NewStorage: func(string, string, string) (marketfetch.KlineRowWriter, error) { return writer, nil },
		NewGetter:  func() markethttp.Getter { return testGetter{} },
		RunSymbolSnapshot: func(context.Context, Request, marketfetch.Storage, *markets.Composition, string) (*model.Response, error) {
			return &model.Response{Success: false, Message: "exchange snapshot unavailable"}, nil
		},
		PublishCompletion: func(_ context.Context, _ Request, _ string, results []itemResult) error {
			if len(results) != 1 {
				t.Fatalf("completion results=%v", results)
			}
			completionErr = results[0].err
			return nil
		},
		Now: time.Now,
	}
	raw, err := json.Marshal(map[string]interface{}{
		"action": "market_fetch", "request_id": "failed-symbol-event", "storage_rpc_gateway_target": "ip://127.0.0.1:11003",
		"data": map[string]interface{}{
			"space_id": "crypto", "dataset_id": "binance_spot_symbols", "market_id": "crypto", "instrument_type": "spot",
			"provider_id": "binance", "source_id": "spot_http", "data_type": "symbol", "batch_kind": "symbol_snapshot", "schedule_id": "symbol-schedule", "frequency": "1H", "source_event_id": "failed-symbol-event",
			"items": []map[string]string{{"subject_id": "binance_spot_symbols", "task_id": "task-symbol"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := handler.HandleRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got := response.(Response)
	if got.Success || got.Failed != 1 || completionErr == nil || completionErr.Error() != "exchange snapshot unavailable" {
		t.Fatalf("failed symbol response=%+v completion_err=%v", got, completionErr)
	}
}

func TestSymbolResponsePreservesInstrumentRowsWritten(t *testing.T) {
	got := symbolResponse(&model.Response{
		Success:     true,
		RowsWritten: 37,
		Data:        &marketfetchpb.MarketFetchBatchCompleted{Items: []*marketfetchpb.MarketFetchItemResult{{Outcome: "success"}}},
	}, "event-1", "now")
	if !got.Success || got.RowsWritten != 37 || got.Failed != 0 {
		t.Fatalf("symbol response=%+v", got)
	}
}

func TestHandlerPublishesCompletionForInvokedKline(t *testing.T) {
	writer := &sourceTestWriter{}
	called := false
	handler := &Handler{
		NewStorage: func(string, string, string) (marketfetch.KlineRowWriter, error) { return writer, nil },
		NewGetter:  func() markethttp.Getter { return testGetter{} },
		PublishCompletion: func(_ context.Context, request Request, requestID string, results []itemResult) error {
			called = true
			if request.BatchKind != "catchup" || requestID != "catchup-event" || len(request.Items) != 1 || request.Items[0].TaskID != "task-1" || len(results) != 1 || results[0].err != nil || results[0].last.IsZero() {
				t.Fatalf("unexpected completion input: request=%+v request_id=%q results=%+v", request, requestID, results)
			}
			return nil
		},
		Now: time.Now,
	}
	raw, err := json.Marshal(map[string]interface{}{
		"action":                     "market_fetch",
		"request_id":                 "catchup-event",
		"storage_rpc_gateway_target": "ip://127.0.0.1:11003",
		"data": map[string]interface{}{
			"space_id": "stock_cn", "dataset_id": "stock_cn_kline", "market_id": "stock_cn", "instrument_type": "equity",
			"provider_id": "eastmoney", "source_id": "stock_cn_http", "batch_kind": "catchup", "frequency": "1m", "source_event_id": "catchup-event",
			"items": []map[string]string{{"subject_id": "SH.600000", "provider_symbol": "SH.600000", "task_id": "task-1"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := handler.HandleRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got := response.(Response)
	if !called || !got.Success || got.RowsWritten != 1 {
		t.Fatalf("unexpected invoked kline response=%+v called=%v", got, called)
	}
}

func TestDefaultSourceMapping(t *testing.T) {
	for _, test := range []struct {
		market, instrument, source string
	}{
		{"stock_cn", "equity", "stock_cn_http"},
		{"stock_hk", "equity", "stock_hk_http"},
		{"stock_us", "equity", "stock_us_http"},
		{"stock_cn", "index", "index_http"},
		{"stock_cn", "convertible_bond", "convertible_bond_http"},
		{"crypto", "spot", "spot_http"},
		{"crypto", "swap", "swap_http"},
	} {
		_, source := defaultSource(Request{MarketID: test.market, InstrumentType: test.instrument})
		if source != test.source {
			t.Fatalf("default source for %s/%s = %q, want %q", test.market, test.instrument, source, test.source)
		}
	}
}

func TestTDXInvocationBudgetReservesFallbackAttempts(t *testing.T) {
	if got, want := tdxInvocationBudget(15*time.Second, 2, 2), 181*time.Second; got != want {
		t.Fatalf("tdx invocation budget = %s, want %s", got, want)
	}
	if got := tdxInvocationBudget(0, 2, 2); got != 0 {
		t.Fatalf("zero per-attempt timeout budget = %s, want zero", got)
	}
	if got := tdxInvocationBudget(15*time.Second, 0, 2); got != 0 {
		t.Fatalf("zero route budget = %s, want zero", got)
	}
	if got := tdxInvocationBudget(15*time.Second, 2, 0); got != 0 {
		t.Fatalf("zero item budget = %s, want zero", got)
	}
}

func TestShouldReconnectTDXOnlyForWireFailures(t *testing.T) {
	if !shouldReconnectTDX(tdxwire.ErrTransport) {
		t.Fatal("transport failure should trigger TDX reconnect")
	}
	if !shouldReconnectTDX(tdxwire.ErrProtocol) {
		t.Fatal("protocol failure should trigger TDX reconnect")
	}
	if shouldReconnectTDX(errors.New("storage write failed")) {
		t.Fatal("storage failure should not trigger TDX reconnect")
	}
}

func TestDefaultSourceDoesNotRewriteExplicitCryptoProvider(t *testing.T) {
	provider, source := defaultSource(Request{MarketID: "crypto", InstrumentType: "spot", ProviderID: "sina"})
	if provider != "sina" || source != "" {
		t.Fatalf("explicit crypto provider was rewritten: provider=%q source=%q", provider, source)
	}
}

func TestDefaultSourceUsesBinanceForImplicitCryptoProvider(t *testing.T) {
	provider, source := defaultSource(Request{MarketID: "crypto", InstrumentType: "spot"})
	if provider != "binance" || source != "spot_http" {
		t.Fatalf("implicit crypto source = %q/%q, want binance/spot_http", provider, source)
	}
}

func TestRequestRejectsSourceWithoutProvider(t *testing.T) {
	request := Request{SpaceID: "stock_cn", DatasetID: "stock_cn_kline", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "stock_cn_http", SourceEventID: "event-1", Frequency: "1d", Items: []Item{{SubjectID: "SH.600000", ProviderSymbol: "SH.600000"}}}
	if err := request.validate(); err == nil || !strings.Contains(err.Error(), "provider_id is required") {
		t.Fatalf("source without provider should fail validation, err=%v", err)
	}
}

func TestDefaultSourceMapsConcreteStockProviders(t *testing.T) {
	tests := []struct {
		name, market, instrument, provider, source string
	}{
		{name: "tdx stock", market: "stock_cn", instrument: "equity", provider: "tdx", source: "normal_7709"},
		{name: "tdx index", market: "stock_cn", instrument: "index", provider: "tdx", source: "normal_7709"},
		{name: "tencent stock", market: "stock_cn", instrument: "equity", provider: "tencent", source: "stock_cn_http"},
		{name: "ths stock", market: "stock_cn", instrument: "equity", provider: "ths", source: "daily_http"},
		{name: "sina stock", market: "stock_cn", instrument: "equity", provider: "sina", source: "stock_cn_http"},
		{name: "cni index", market: "stock_cn", instrument: "index", provider: "cni", source: "index_cni_http"},
		{name: "sw index", market: "stock_cn", instrument: "index", provider: "sw", source: "index_sw_http"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, source := defaultSource(Request{MarketID: test.market, InstrumentType: test.instrument, ProviderID: test.provider})
			if provider != test.provider || source != test.source {
				t.Fatalf("source = %q/%q, want %q/%q", provider, source, test.provider, test.source)
			}
		})
	}
}

func TestHandlerBuildsRequestFromStaticTimerAssignment(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stock_cn")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_ID", "stock_cn")
	t.Setenv("MOOX_MARKET_FETCH_INSTRUMENT_TYPE", "equity")
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "eastmoney")
	t.Setenv("MOOX_MARKET_FETCH_SOURCE_ID", "stock_cn_http")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "stock_cn_kline")
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_ASSIGNMENT_HASH", "assignment-1")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "SH.600000|SH.600001")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"SH.600000":"SH.600000","SH.600001":"SH.600001"}`)

	writer := &sourceTestWriter{}
	handler := &Handler{
		NewStorage: func(string, string, string) (marketfetch.KlineRowWriter, error) { return writer, nil },
		NewGetter:  func() markethttp.Getter { return testGetter{} },
		Now:        func() time.Time { return time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC) },
	}
	raw, err := json.Marshal(model.CloudFunctionEvent{
		Type: "Timer", TriggerName: timerTriggerName, Time: "2026-08-31T10:00:00Z", Message: timerTriggerMessage,
		RequestID: "timer-request", StorageRPCGatewayTarget: "ip://127.0.0.1:11003",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := handler.HandleRequest(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	got := response.(Response)
	if !got.Success || got.RowsWritten != 2 || writer.rows != 2 {
		t.Fatalf("unexpected timer response=%+v rows=%d", got, writer.rows)
	}
	if len(writer.sources) != 2 || writer.sources[0] == writer.sources[1] || !strings.HasPrefix(writer.sources[0], "timer:assignment-1:") {
		t.Fatalf("timer source event ids=%v", writer.sources)
	}
}

func TestItemSourceEventIDIsStableAndPerPayload(t *testing.T) {
	first := itemSourceEventID("batch-1", "SH.600000")
	if first == itemSourceEventID("batch-1", "SH.600001") {
		t.Fatal("different payloads must not share a source event id")
	}
	if first != itemSourceEventID("batch-1", "SH.600000") {
		t.Fatal("transport retries must reuse the same source event id")
	}
}

func TestRequestRejectsDuplicateSubjects(t *testing.T) {
	request := Request{
		SpaceID: "stock_cn", DatasetID: "stock_cn_kline", MarketID: "stock_cn", InstrumentType: "equity",
		SourceEventID: "event-1", Frequency: "1m", Items: []Item{{SubjectID: "SH.600000", ProviderSymbol: "SH.600000"}, {SubjectID: " sh.600000 ", ProviderSymbol: "SH.600000"}},
	}
	if err := request.validate(); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate subjects should fail validation, err=%v", err)
	}
}

func TestRequestRejectsDuplicateSourceEventIDs(t *testing.T) {
	request := Request{
		SpaceID: "stock_cn", DatasetID: "stock_cn_kline", MarketID: "stock_cn", InstrumentType: "equity",
		SourceEventID: "event-1", Frequency: "1m", Items: []Item{
			{SubjectID: "SH.600000", ProviderSymbol: "SH.600000", SourceEventID: "item-event"},
			{SubjectID: "SH.600001", ProviderSymbol: "SH.600001", SourceEventID: " item-event "},
		},
	}
	request.normalize()
	if err := request.validate(); err == nil || !strings.Contains(err.Error(), "source_event_id duplicates") {
		t.Fatalf("duplicate item source event ids should fail validation, err=%v", err)
	}
}

func TestRequestRejectsMultipleSymbolItems(t *testing.T) {
	request := Request{
		SpaceID: "crypto", DatasetID: "binance_spot_symbols", MarketID: "crypto", InstrumentType: "spot",
		DataType: "symbol", SourceEventID: "event-1", Frequency: "1h", Items: []Item{
			{SubjectID: "symbols-a"}, {SubjectID: "symbols-b"},
		},
	}
	request.normalize()
	if err := request.validate(); err == nil || !strings.Contains(err.Error(), "exactly one item") {
		t.Fatalf("multiple symbol items should fail validation, err=%v", err)
	}
}

func TestGenericItemOutcomeTreatsTDXProtocolErrorsAsRetryable(t *testing.T) {
	outcome, errorType := genericItemOutcome(fmt.Errorf("invalid frame: %w", tdxwire.ErrProtocol))
	if outcome != "network_error" || errorType != "tdx_protocol" {
		t.Fatalf("TDX protocol outcome = %q/%q, want network_error/tdx_protocol", outcome, errorType)
	}
}

func TestEnvNonNegativeIntPreservesExplicitZero(t *testing.T) {
	t.Setenv("MOOX_TEST_NON_NEGATIVE", "0")
	if got := envNonNegativeInt("MOOX_TEST_NON_NEGATIVE", 10); got != 0 {
		t.Fatalf("explicit zero = %d, want 0", got)
	}
	t.Setenv("MOOX_TEST_NON_NEGATIVE", "-1")
	if got := envNonNegativeInt("MOOX_TEST_NON_NEGATIVE", 10); got != 10 {
		t.Fatalf("negative value = %d, want fallback 10", got)
	}
}

func TestParseTDXRouteAddressesCanonicalizesAndDeduplicates(t *testing.T) {
	addresses, err := parseTDXRouteAddresses(`["192.0.2.10", "2001:db8::1", "192.0.2.10"]`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(addresses, ","), "192.0.2.10,2001:db8::1"; got != want {
		t.Fatalf("addresses = %q, want %q", got, want)
	}
}

func TestParseTDXRouteAddressesRejectsHostAndPort(t *testing.T) {
	if _, err := parseTDXRouteAddresses(`["quotes.example:7709"]`); err == nil {
		t.Fatal("TDX route list must contain bare IP addresses")
	}
}

var _ marketfetch.KlineRowWriter = (*testWriter)(nil)
var _ interface {
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
} = (*sourceTestWriter)(nil)

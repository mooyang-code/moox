package marketdata

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type testWriter struct {
	mu   sync.Mutex
	rows int
}

type sourceTestWriter struct {
	testWriter
	sources []string
}

func (writer *testWriter) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.rows += len(rows)
	return nil
}

func (writer *sourceTestWriter) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, source string) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.rows += len(rows)
	writer.sources = append(writer.sources, source)
	return nil
}

type testGetter struct{}

func (testGetter) Get(_ context.Context, _ string, _ string, _ url.Values, result interface{}) error {
	return json.Unmarshal([]byte(`{"rc":0,"data":{"klines":["2026-08-31 09:30,10,10.5,11,9.5,100,1050"]}}`), result)
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

func TestDefaultSourceMapping(t *testing.T) {
	for _, test := range []struct {
		market, instrument, source string
	}{
		{"stock_cn", "equity", "stock_cn_http"},
		{"stock_hk", "equity", "stock_hk_http"},
		{"stock_us", "equity", "stock_us_http"},
		{"stock_cn", "index", "index_http"},
		{"stock_cn", "convertible_bond", "convertible_bond_http"},
	} {
		_, source := defaultSource(Request{MarketID: test.market, InstrumentType: test.instrument})
		if source != test.source {
			t.Fatalf("default source for %s/%s = %q, want %q", test.market, test.instrument, source, test.source)
		}
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
	if len(writer.sources) != 2 || writer.sources[0] == writer.sources[1] || !strings.HasPrefix(writer.sources[0], "scf:timer-request") {
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

var _ marketfetch.KlineRowWriter = (*testWriter)(nil)
var _ interface {
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
} = (*sourceTestWriter)(nil)

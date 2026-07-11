package test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/coverage"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	cryptomarket "github.com/mooyang-code/moox/modules/collector/internal/markets/crypto"
	"github.com/mooyang-code/moox/modules/collector/internal/pipeline"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	binanceprovider "github.com/mooyang-code/moox/modules/collector/internal/providers/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/modules/storage/testkit"
)

func TestCryptoSwapPipelineE2E_ProviderToPebble(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/klines" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[[1783728000000,"1","2","0.5","1.5","3",1783728059999,"4",1,"0","0","0"]]`))
	}))
	defer server.Close()
	access, err := testkit.Open(t.TempDir(), []testkit.DatasetSchema{marketDatasetSchema("crypto_binance", "binance_kline", false), marketDatasetSchema("crypto_binance", "swap_kline", true), qualityDatasetSchema("crypto_binance", "kline_quality_event")})
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{{SpaceID: "crypto_binance", DatasetID: "binance_kline", Role: storageio.RoleProviderData, Feed: "kline", ProviderID: "binance"}, {SpaceID: "crypto_binance", DatasetID: "swap_kline", Role: storageio.RoleUnifiedData, Feed: "kline", RequiredVolume: true, RequiredAmount: true}, {SpaceID: "crypto_binance", DatasetID: "kline_quality_event", Role: storageio.RoleQualityEvent, Feed: "quality_event"}})
	provider := binanceprovider.New(binanceprovider.Config{BaseURL: server.URL, FuturesBaseURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return fixedE2ETime.Add(time.Hour) }})
	pipe := pipeline.KlinePipeline{Provider: provider, Gate: providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, Store: store, SpaceID: "crypto_binance", SourceDatasetID: "binance_kline", SourceDatasetIDs: []string{"binance_kline"}, SourceDatasets: map[marketdata.ProviderID]string{"binance": "binance_kline"}, UnifiedDatasetID: "swap_kline", QualityDatasetID: "kline_quality_event", Resolver: pipeline.QualityResolver{Policy: pipeline.QualityPolicy{ProviderPriority: []marketdata.ProviderID{"binance"}, AuthoritativeSingleSource: true}, Now: func() time.Time { return fixedE2ETime }}}
	result, err := pipe.Run(context.Background(), providers.FetchKlinesRequest{MarketID: "crypto_binance", ProductType: marketdata.ProductSwap, InstrumentType: marketdata.InstrumentSwap, Frequency: marketdata.FrequencyMinute, Subjects: []providers.ProviderSubject{{SubjectID: "BTC-USDT-SWAP", ProviderSymbol: "BTCUSDT"}}})
	if err != nil || result.SourceRows != 1 || result.UnifiedRows != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	row, err := store.Unified(context.Background(), "crypto_binance", "swap_kline", "BTC-USDT-SWAP", marketdata.FrequencyMinute, time.UnixMilli(1783728000000).UTC())
	if err != nil || row == nil || row.ProviderID != "binance" || row.FeedScope != "swap" || row.Close.String() != "1.5" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}

// This is a module-level E2E test: a Provider result travels through the same
// source-first Pipeline and whole-candle resolver used by an SCF JobItem, while
// both source and unified facts are persisted by the real Pebble primary store.
func TestMarketPipelineE2E_SourceFirstFallbackAndIdempotentRetry(t *testing.T) {
	access, err := testkit.Open(t.TempDir(), []testkit.DatasetSchema{
		marketDatasetSchema("crypto_binance", "primary_kline", false),
		marketDatasetSchema("crypto_binance", "fallback_kline", false),
		marketDatasetSchema("crypto_binance", "spot_kline", true),
		qualityDatasetSchema("crypto_binance", "kline_quality_event"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = access.Close() })
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{
		{SpaceID: "crypto_binance", DatasetID: "primary_kline", Role: storageio.RoleProviderData, Feed: "kline", ProviderID: "primary", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, RequiredVolume: true, VolumeUnit: "base", AmountUnit: "quote"},
		{SpaceID: "crypto_binance", DatasetID: "fallback_kline", Role: storageio.RoleProviderData, Feed: "kline", ProviderID: "fallback", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, RequiredVolume: true, VolumeUnit: "base", AmountUnit: "quote"},
		{SpaceID: "crypto_binance", DatasetID: "spot_kline", Role: storageio.RoleUnifiedData, Feed: "kline", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, RequiredVolume: true, RequiredAmount: true, VolumeUnit: "base", AmountUnit: "quote"},
		{SpaceID: "crypto_binance", DatasetID: "kline_quality_event", Role: storageio.RoleQualityEvent, Feed: "quality_event"},
	})
	primary := &e2EProvider{id: "primary", rows: []marketdata.ProviderKline{e2EKline("primary", "10")}}
	pipe := pipeline.KlinePipeline{Provider: primary, Gate: providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, Store: store, SpaceID: "crypto_binance", SourceDatasetID: "primary_kline", SourceDatasetIDs: []string{"primary_kline", "fallback_kline"}, SourceDatasets: map[marketdata.ProviderID]string{"primary": "primary_kline", "fallback": "fallback_kline"}, UnifiedDatasetID: "spot_kline", QualityDatasetID: "kline_quality_event", Resolver: pipeline.QualityResolver{Policy: pipeline.QualityPolicy{ProviderPriority: []marketdata.ProviderID{"primary", "fallback"}, AuthoritativeSingleSource: true}, Now: func() time.Time { return fixedE2ETime }}}
	req := providers.FetchKlinesRequest{MarketID: "crypto_binance", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Frequency: marketdata.FrequencyHour, Subjects: []providers.ProviderSubject{{SubjectID: "BTC-USDT", ProviderSymbol: "BTCUSDT"}}}
	if summary, err := pipe.Run(context.Background(), req); err != nil || summary.SourceRows != 1 || summary.UnifiedRows != 1 {
		t.Fatalf("first run summary=%+v err=%v", summary, err)
	}
	if got := readUnified(t, store); got == nil || got.ProviderID != "primary" || got.Close.String() != "10" || got.SourceDatasetID != "primary_kline" {
		t.Fatalf("source-first write failed: %+v", got)
	}
	if _, err := pipe.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := readUnified(t, store); got == nil || got.Revision != 1 {
		t.Fatalf("retry must replace the same Pebble row and preserve revision: %+v", got)
	}
	fallback := &e2EProvider{id: "fallback", rows: []marketdata.ProviderKline{e2EKline("fallback", "11")}}
	pipe.Provider = fallback
	pipe.SourceDatasetID = "fallback_kline"
	if _, err := pipe.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := readUnified(t, store); got == nil || got.ProviderID != "primary" || got.Close.String() != "10" || got.SourceDatasetID != "primary_kline" {
		t.Fatalf("fallback must not mix or replace primary whole candle: %+v", got)
	}
}

func TestInstrumentPipelineE2E_SourceAndUnifiedRecordsUseStableGeneration(t *testing.T) {
	generation := fixedE2ETime
	access, err := testkit.Open(t.TempDir(), []testkit.DatasetSchema{instrumentDatasetSchema("crypto_binance", "binance_instruments", false), instrumentDatasetSchema("crypto_binance", "instruments", true)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = access.Close() })
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{{SpaceID: "crypto_binance", DatasetID: "binance_instruments", Role: storageio.RoleProviderData, Feed: "instrument", ProviderID: "binance"}, {SpaceID: "crypto_binance", DatasetID: "instruments", Role: storageio.RoleUnifiedData, Feed: "instrument"}})
	value := providers.ProviderInstrument{SubjectID: "BTC-USDT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", ExchangeID: "BINANCE", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, Name: "BTC/USDT", Currency: "USDT", Status: "trading", EffectiveAt: generation, FetchedAt: generation, RequestID: "exchange-info"}
	pipe := pipeline.InstrumentPipeline{Provider: e2EInstrumentProvider{value: value}, Gate: providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, Store: store, SpaceID: "crypto_binance", SourceDatasetID: "binance_instruments", SourceDatasetIDs: []string{"binance_instruments"}, SourceDatasets: map[marketdata.ProviderID]string{"binance": "binance_instruments"}, UnifiedDatasetID: "instruments", ProviderPriority: []marketdata.ProviderID{"binance"}, Generation: generation, Now: func() time.Time { return generation.Add(time.Second) }}
	for run := 0; run < 2; run++ {
		summary, err := pipe.Run(context.Background(), providers.FetchInstrumentsRequest{MarketID: "crypto_binance", ExchangeID: "BINANCE", SnapshotAt: generation})
		if err != nil || summary.SourceRows != 1 || summary.UnifiedRows != 1 {
			t.Fatalf("run=%d summary=%+v err=%v", run, summary, err)
		}
	}
	rsp, err := access.ReadRecordRows(context.Background(), &storagepb.ReadRecordRowsReq{Keys: []*storagepb.RecordKey{{SpaceId: "crypto_binance", DatasetId: "instruments", RecordId: "BTC-USDT", Version: generation.Format(time.RFC3339Nano)}}})
	if err != nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 {
		t.Fatalf("read unified instrument rsp=%+v err=%v", rsp, err)
	}
}

func TestCalendarPipelineE2E_MaterializesPolicyIntoPebble(t *testing.T) {
	generation := fixedE2ETime
	access, err := testkit.Open(t.TempDir(), []testkit.DatasetSchema{calendarDatasetSchema("crypto_binance", "calendar")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = access.Close() })
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{{SpaceID: "crypto_binance", DatasetID: "calendar", Role: storageio.RoleUnifiedData, Feed: "calendar"}})
	pipe := pipeline.CalendarPipeline{Policy: cryptomarket.CalendarPolicy{TwentyFourSeven: true, ExchangeID: "BINANCE"}, Store: store, DatasetID: "calendar", Generation: generation}
	result, err := pipe.Materialize(context.Background(), pipeline.CalendarRequest{Start: generation, End: generation.Add(24 * time.Hour), Limit: 10})
	if err != nil || result.Rows != 2 || !result.Complete {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	rsp, err := access.ReadRecordRows(context.Background(), &storagepb.ReadRecordRowsReq{Keys: []*storagepb.RecordKey{{SpaceId: "crypto_binance", DatasetId: "calendar", RecordId: "BINANCE|2026-07-11", Version: generation.Format(time.RFC3339Nano)}}})
	if err != nil || len(rsp.GetRows()) != 1 {
		t.Fatalf("rsp=%+v err=%v", rsp, err)
	}
}

func TestCoverageReconcilerE2E_DetectsPebbleGapAndPersistsRepairState(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	access, err := testkit.Open(t.TempDir(), []testkit.DatasetSchema{marketDatasetSchema("crypto_binance", "spot_kline", true), coverageDatasetSchema("crypto_binance", "market_coverage")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = access.Close() })
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{{SpaceID: "crypto_binance", DatasetID: "spot_kline", Role: storageio.RoleUnifiedData, Feed: "kline", RequiredVolume: true, RequiredAmount: true, VolumeUnit: "base", AmountUnit: "quote"}, {SpaceID: "crypto_binance", DatasetID: "market_coverage", Role: storageio.RoleCoverageState, Feed: "coverage"}})
	for _, offset := range []time.Duration{0, 2 * time.Minute} {
		row := e2EKline("binance", "10")
		row.Frequency = marketdata.FrequencyMinute
		row.DataTime = base.Add(offset)
		row.CloseTime = row.DataTime.Add(time.Minute)
		row.TradeDate = "2026-07-11"
		if err := store.WriteUnifiedKline(context.Background(), "spot_kline", marketdata.ResolvedKline{ProviderKline: row, SourceDatasetID: "binance_kline", QualityStatus: "accepted", Revision: 1, ResolvedAt: row.CloseTime}); err != nil {
			t.Fatal(err)
		}
	}
	repairs := &e2ERepairSink{}
	reconciler := coverage.Reconciler{Reader: store, States: store, Repairs: repairs, Now: func() time.Time { return base.Add(time.Hour) }}
	state, err := reconciler.Reconcile(context.Background(), coverage.Request{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", PartitionID: "2026-07-11T00:00", Frequency: marketdata.FrequencyMinute, Start: base, End: base.Add(3 * time.Minute), Sessions: []coverage.Session{{TradeDate: "2026-07-11", Open: base, Close: base.Add(3 * time.Minute), DailyAnchor: base}}})
	if err != nil {
		t.Fatal(err)
	}
	if state.Missing != 1 || len(repairs.values) != 1 || !repairs.values[0].Start.Equal(base.Add(time.Minute)) {
		t.Fatalf("state=%+v repairs=%+v", state, repairs.values)
	}
	keyHash := sha256.Sum256([]byte("spot_kline|BTC-USDT|1m|2026-07-11T00:00"))
	rsp, err := access.ReadRecordRows(context.Background(), &storagepb.ReadRecordRowsReq{Keys: []*storagepb.RecordKey{{SpaceId: "crypto_binance", DatasetId: "market_coverage", RecordId: hex.EncodeToString(keyHash[:]), Version: base.Format(time.RFC3339Nano)}}})
	if err != nil || len(rsp.GetRows()) != 1 {
		t.Fatalf("rsp=%+v err=%v", rsp, err)
	}
}

func readUnified(t *testing.T, store *storageio.Client) *marketdata.ResolvedKline {
	t.Helper()
	value, err := store.Unified(context.Background(), "crypto_binance", "spot_kline", "BTC-USDT", marketdata.FrequencyHour, fixedE2ETime.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func marketDatasetSchema(spaceID, datasetID string, unified bool) testkit.DatasetSchema {
	columns := map[string]storagepb.FieldValueType{}
	for _, name := range []string{"open", "high", "low", "close", "volume", "amount"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
		columns[name+"_exact"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	for _, name := range []string{"trade_date", "feed_scope", "volume_unit", "amount_unit", "request_id"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	for _, name := range []string{"close_time", "provider_timestamp", "fetched_at"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
	}
	columns["is_closed"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL
	if unified {
		columns["source_provider"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
		columns["source_dataset_id"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
		columns["source_fetched_at"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
		columns["quality_status"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
		columns["revision"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_INT
		columns["resolved_at"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
	}
	return testkit.DatasetSchema{SpaceID: spaceID, DatasetID: datasetID, Columns: columns}
}

func instrumentDatasetSchema(spaceID, datasetID string, unified bool) testkit.DatasetSchema {
	columns := map[string]storagepb.FieldValueType{}
	for _, name := range []string{"subject_id", "provider_id", "provider_symbol", "exchange_id", "product_type", "instrument_type", "name", "currency", "listing_date", "delisting_date", "status", "request_id"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	for _, name := range []string{"effective_at", "fetched_at"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
	}
	if unified {
		for _, name := range []string{"source_provider", "source_dataset_id", "quality_status"} {
			columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
		}
		for _, name := range []string{"generation", "resolved_at"} {
			columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
		}
	}
	return testkit.DatasetSchema{SpaceID: spaceID, DatasetID: datasetID, Columns: columns}
}

func calendarDatasetSchema(spaceID, datasetID string) testkit.DatasetSchema {
	columns := map[string]storagepb.FieldValueType{"exchange_id": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "trade_date": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "timezone": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "session_status": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "sessions_json": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "open_time": storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME, "close_time": storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME, "generation": storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME}
	return testkit.DatasetSchema{SpaceID: spaceID, DatasetID: datasetID, Columns: columns}
}

func coverageDatasetSchema(spaceID, datasetID string) testkit.DatasetSchema {
	columns := map[string]storagepb.FieldValueType{}
	for _, name := range []string{"unified_dataset_id", "subject_id", "frequency", "partition_id", "missing_ranges", "coverage_status"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	for _, name := range []string{"range_start", "range_end", "checked_at"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
	}
	for _, name := range []string{"expected_count", "present_count", "missing_count"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_INT
	}
	return testkit.DatasetSchema{SpaceID: spaceID, DatasetID: datasetID, Columns: columns}
}

func qualityDatasetSchema(spaceID, datasetID string) testkit.DatasetSchema {
	columns := map[string]storagepb.FieldValueType{
		"subject_id": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "frequency": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"data_time": storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME, "event_type": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"provider_ids": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "reason": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"selected_provider": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "source_dataset_id": storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		"revision": storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, "resolved_at": storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME,
	}
	return testkit.DatasetSchema{SpaceID: spaceID, DatasetID: datasetID, Columns: columns}
}

var fixedE2ETime = time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)

func e2EKline(id, close string) marketdata.ProviderKline {
	c := marketdata.MustDecimal(close)
	volume := marketdata.MustDecimal("2")
	amount := marketdata.MustDecimal("20")
	return marketdata.ProviderKline{SubjectID: "BTC-USDT", ProviderID: marketdata.ProviderID(id), ProviderSymbol: "BTCUSDT", Frequency: marketdata.FrequencyHour, DataTime: fixedE2ETime.Add(-time.Hour), CloseTime: fixedE2ETime, TradeDate: "2026-07-11", FeedScope: "spot", VolumeUnit: "base", AmountUnit: "quote", Open: marketdata.MustDecimal("9"), High: marketdata.MustDecimal("12"), Low: marketdata.MustDecimal("8"), Close: c, Volume: &volume, Amount: &amount, ProviderTimestamp: fixedE2ETime, FetchedAt: fixedE2ETime, RequestID: string(id), Closed: true}
}

type e2EProvider struct {
	id   marketdata.ProviderID
	rows []marketdata.ProviderKline
}

type e2EInstrumentProvider struct{ value providers.ProviderInstrument }
type e2ERepairSink struct{ values []coverage.RepairRequest }

func (s *e2ERepairSink) EnqueueRepair(_ context.Context, value coverage.RepairRequest) error {
	s.values = append(s.values, value)
	return nil
}

func (p e2EInstrumentProvider) ID() marketdata.ProviderID          { return p.value.ProviderID }
func (e2EInstrumentProvider) Capabilities() []providers.Capability { return nil }
func (p e2EInstrumentProvider) FetchInstruments(context.Context, providers.RequestGate, providers.FetchInstrumentsRequest) (providers.FetchInstrumentsResult, error) {
	return providers.FetchInstrumentsResult{Instruments: []providers.ProviderInstrument{p.value}, Complete: true, RequestCount: 1}, nil
}

func (p *e2EProvider) ID() marketdata.ProviderID          { return p.id }
func (*e2EProvider) Capabilities() []providers.Capability { return nil }
func (p *e2EProvider) FetchKlines(context.Context, providers.RequestGate, providers.FetchKlinesRequest) (providers.FetchKlinesResult, error) {
	return providers.FetchKlinesResult{Rows: p.rows, Complete: true, RequestCount: 1}, nil
}

package test

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/pipeline"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/modules/storage/testkit"
)

// This is a module-level E2E test: a Provider result travels through the same
// source-first Pipeline and whole-candle resolver used by an SCF JobItem, while
// both source and unified facts are persisted by the real Pebble primary store.
func TestMarketPipelineE2E_SourceFirstFallbackAndIdempotentRetry(t *testing.T) {
	access, err := testkit.Open(t.TempDir(), []testkit.DatasetSchema{
		marketDatasetSchema("crypto_binance", "primary_kline", false),
		marketDatasetSchema("crypto_binance", "fallback_kline", false),
		marketDatasetSchema("crypto_binance", "spot_kline", true),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = access.Close() })
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{
		{SpaceID: "crypto_binance", DatasetID: "primary_kline", Role: storageio.RoleProviderData, Feed: "kline", ProviderID: "primary", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, RequiredVolume: true, VolumeUnit: "base", AmountUnit: "quote"},
		{SpaceID: "crypto_binance", DatasetID: "fallback_kline", Role: storageio.RoleProviderData, Feed: "kline", ProviderID: "fallback", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, RequiredVolume: true, VolumeUnit: "base", AmountUnit: "quote"},
		{SpaceID: "crypto_binance", DatasetID: "spot_kline", Role: storageio.RoleUnifiedData, Feed: "kline", ProductType: marketdata.ProductSpot, InstrumentType: marketdata.InstrumentSpot, RequiredVolume: true, RequiredAmount: true, VolumeUnit: "base", AmountUnit: "quote"},
	})
	primary := &e2EProvider{id: "primary", rows: []marketdata.ProviderKline{e2EKline("primary", "10")}}
	pipe := pipeline.KlinePipeline{Provider: primary, Gate: providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}, Store: store, SpaceID: "crypto_binance", SourceDatasetID: "primary_kline", SourceDatasetIDs: []string{"primary_kline", "fallback_kline"}, SourceDatasets: map[marketdata.ProviderID]string{"primary": "primary_kline", "fallback": "fallback_kline"}, UnifiedDatasetID: "spot_kline", Resolver: pipeline.QualityResolver{Policy: pipeline.QualityPolicy{ProviderPriority: []marketdata.ProviderID{"primary", "fallback"}, AuthoritativeSingleSource: true}, Now: func() time.Time { return fixedE2ETime }}}
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

func (p *e2EProvider) ID() marketdata.ProviderID          { return p.id }
func (*e2EProvider) Capabilities() []providers.Capability { return nil }
func (p *e2EProvider) FetchKlines(context.Context, providers.RequestGate, providers.FetchKlinesRequest) (providers.FetchKlinesResult, error) {
	return providers.FetchKlinesResult{Rows: p.rows, Complete: true, RequestCount: 1}, nil
}

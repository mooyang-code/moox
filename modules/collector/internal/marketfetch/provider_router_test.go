package marketfetch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
)

func TestFallbackErrorClassification(t *testing.T) {
	if fallbackError(context.Canceled) {
		t.Fatal("context cancellation must not trigger provider fallback")
	}
	if !fallbackError(marketdata.ErrUnavailable) {
		t.Fatal("source unavailable should trigger provider fallback")
	}
	if fallbackError(errors.New("provider_symbol is required")) {
		t.Fatal("invalid caller input must not trigger provider fallback")
	}
}

func TestProviderRouterDispatchesBySourceKey(t *testing.T) {
	key := marketdata.SourceKey{ProviderID: "eastmoney", SourceID: "stock_cn_http"}
	fetcher := pipelineFetcher{
		descriptor: marketdata.ProviderDescriptor{ProviderID: key.ProviderID, SourceID: key.SourceID, ProtocolVariant: marketdata.ProtocolHTTP, Transport: "https", Port: 443, Markets: []string{"stock_cn"}, InstrumentTypes: []string{"equity"}, Frequencies: []string{"1d"}},
		spec:       marketdata.KlineSpec{MarketID: "stock_cn", InstrumentType: "equity", Frequencies: []string{"1d"}, CompleteOHLCV: true, VolumeUnit: "share", MaxBarsPerRequest: 10, RequestTimeoutSeconds: 1},
		bars: []marketdata.NormalizedKline{{
			SubjectID: "SH.600000", ProviderID: key.ProviderID, SourceID: key.SourceID, ProviderSymbol: "SH.600000", Frequency: "1d",
			BarStart: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), BarEnd: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			Open: common.NewDecimal("10"), High: common.NewDecimal("11"), Low: common.NewDecimal("9"), Close: common.NewDecimal("10.5"), Volume: common.NewDecimal("100"), VolumeUnit: "share",
		}},
	}
	registry := sources.NewProviderRegistry()
	if err := registry.Register(sources.ProviderRegistration{Descriptor: fetcher.Descriptor(), Klines: fetcher}); err != nil {
		t.Fatal(err)
	}
	writer := &pipelineWriter{}
	result, err := (&ProviderRouter{Registry: registry, Writer: writer}).FetchAndWrite(context.Background(), PipelineRequest{SourceKey: key, SpaceID: "stock_cn", DatasetID: "stock_cn_kline", SourceEventID: "event-1", Request: marketdata.KlineRequest{MarketID: "stock_cn", InstrumentType: "equity", SubjectID: "SH.600000", ProviderSymbol: "SH.600000", Frequency: "1d", Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsWritten != 1 || len(writer.rows) != 1 {
		t.Fatalf("router result = %+v rows=%d", result, len(writer.rows))
	}
}

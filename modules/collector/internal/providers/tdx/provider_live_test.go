//go:build live

package tdx

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

func TestLiveTDXInstrumentAndDailyKline(t *testing.T) {
	address := os.Getenv("MOOX_TDX_ADDRESS")
	if address == "" {
		t.Fatal("MOOX_TDX_ADDRESS is required")
	}
	provider := New(Config{Address: address})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	gate := providers.StaticGate{Permit: providers.RequestPermit{Allowed: true}}
	instruments, err := provider.FetchInstruments(ctx, gate, providers.FetchInstrumentsRequest{MarketID: "stock_cn", ExchangeID: "XSHG", InstrumentTypes: []marketdata.InstrumentType{marketdata.InstrumentEquity}, Limit: 20})
	if err != nil || len(instruments.Instruments) == 0 {
		t.Fatalf("instrument probe rows=%d err=%v", len(instruments.Instruments), err)
	}
	for _, frequency := range []marketdata.Frequency{marketdata.FrequencyDay, marketdata.FrequencyMinute} {
		klines, err := provider.FetchKlines(ctx, gate, providers.FetchKlinesRequest{MarketID: "stock_cn", ExchangeID: "XSHG", ProductType: marketdata.ProductEquity, InstrumentType: marketdata.InstrumentEquity, Frequency: frequency, Subjects: []providers.ProviderSubject{{SubjectID: "600000.XSHG", ProviderSymbol: "600000"}}, Limit: 5})
		if err != nil || len(klines.Rows) == 0 {
			t.Fatalf("kline probe frequency=%s rows=%d err=%v", frequency, len(klines.Rows), err)
		}
		for _, row := range klines.Rows {
			if row.ProviderID != "tdx" || row.SubjectID != "600000.XSHG" || !row.Closed || row.Close.String() == "" {
				t.Fatalf("invalid live row: %+v", row)
			}
		}
	}
}

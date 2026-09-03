package convertiblebond

import (
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp"
)

func NewHTTPProvider(providerID, sourceID, displayName, baseURL, host string, symbolFunc markethttp.SymbolFunc, client *http.Client, now func() time.Time) *markethttp.Provider {
	return markethttp.New(markethttp.Config{
		ProviderID: providerID, SourceID: sourceID, DisplayName: displayName,
		MarketID: "stock_cn", InstrumentType: marketdata.InstrumentConvertibleBond,
		Exchanges: []string{"XSHG", "XSHE"}, BaseURL: baseURL,
		Endpoint: "/api/qt/stock/kline/get", Host: host, HTTPClient: client,
		Location: mustLocation("Asia/Shanghai"), SymbolFunc: symbolFunc,
		Frequencies:       []string{"1m", "5m", "15m", "30m", "60m", "1d"},
		MaxBarsPerRequest: 1200, TimestampMode: marketdata.TimestampModeOpen,
		CompleteOHLCV: true, HasAmount: true, Status: marketdata.SourceCatalogOnly, Now: now,
	})
}

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

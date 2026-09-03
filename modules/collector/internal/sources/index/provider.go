package index

import (
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp"
)

func NewHTTPProvider(providerID, sourceID, displayName, baseURL, host string, symbolFunc markethttp.SymbolFunc, client *http.Client, now func() time.Time, status marketdata.SourceStatus) *markethttp.Provider {
	return markethttp.New(markethttp.Config{
		ProviderID: providerID, SourceID: sourceID, DisplayName: displayName,
		MarketID: "stock_cn", InstrumentType: marketdata.InstrumentIndex,
		Exchanges: []string{"XSHG", "XSHE", "XBSE"}, BaseURL: baseURL,
		Endpoint: "/api/qt/stock/kline/get", Host: host, HTTPClient: client,
		Location: mustLocation("Asia/Shanghai"), SymbolFunc: symbolFunc,
		Frequencies: []string{"1d", "1w", "1M"}, MaxBarsPerRequest: 1200,
		TimestampMode: marketdata.TimestampModeOpen, CompleteOHLCV: true,
		HasAmount: true, Status: status, Now: now,
	})
}

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

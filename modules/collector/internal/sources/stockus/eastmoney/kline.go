package eastmoney

import (
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp"
)

type Config struct {
	BaseURL    string
	HTTPClient *http.Client
	Now        func() time.Time
}

func New(cfg Config) *markethttp.Provider {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://push2.eastmoney.com"
	}
	return markethttp.New(markethttp.Config{
		ProviderID: "eastmoney", SourceID: "stock_us_http", DisplayName: "EastMoney US",
		MarketID: "stock_us", InstrumentType: marketdata.InstrumentEquity,
		Exchanges: []string{"XNAS", "XNYS", "XASE"}, BaseURL: cfg.BaseURL,
		Endpoint: "/api/qt/stock/kline/get", Host: "push2.eastmoney.com",
		HTTPClient: cfg.HTTPClient, Location: mustLocation("America/New_York"),
		SymbolFunc: SecID, Frequencies: []string{"1m", "5m", "15m", "30m", "60m", "1d", "1w", "1M"},
		MaxBarsPerRequest: 1200, TimestampMode: marketdata.TimestampModeOpen,
		CompleteOHLCV: true, HasAmount: true, Status: marketdata.SourceCatalogOnly, Now: cfg.Now,
	})
}

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

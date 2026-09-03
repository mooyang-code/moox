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
		ProviderID: "eastmoney", SourceID: "stock_hk_http", DisplayName: "EastMoney Hong Kong",
		MarketID: "stock_hk", InstrumentType: marketdata.InstrumentEquity,
		Exchanges: []string{"XHKG"}, BaseURL: cfg.BaseURL, Endpoint: "/api/qt/stock/kline/get",
		Host: "push2.eastmoney.com", HTTPClient: cfg.HTTPClient, Location: mustLocation("Asia/Hong_Kong"),
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

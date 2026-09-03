package sina

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
		cfg.BaseURL = "https://quotes.sina.cn"
	}
	return markethttp.New(markethttp.Config{
		ProviderID: "sina", SourceID: "stock_us_http", DisplayName: "Sina US",
		MarketID: "stock_us", InstrumentType: marketdata.InstrumentEquity,
		Exchanges: []string{"XNAS", "XNYS", "XASE"}, BaseURL: cfg.BaseURL,
		Endpoint: "/cn/api/jsonp_v2.php/var%20moox_kline=/CN_MarketDataService.getKLineData",
		Host:     "quotes.sina.cn", HTTPClient: cfg.HTTPClient, Location: mustLocation("America/New_York"),
		SymbolFunc: ProviderSymbol, Frequencies: []string{"1d"},
		MaxBarsPerRequest: 1023, TimestampMode: marketdata.TimestampModeOpen,
		CompleteOHLCV: true, HasAmount: false, Status: marketdata.SourceCatalogOnly, Now: cfg.Now,
	})
}

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

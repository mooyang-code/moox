package eastmoney

import (
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	indexsource "github.com/mooyang-code/moox/modules/collector/internal/sources/index"
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
	return indexsource.NewHTTPProvider("eastmoney", "index_http", "EastMoney Index", cfg.BaseURL, "push2.eastmoney.com", indexsource.EastMoneySecID, cfg.HTTPClient, cfg.Now, marketdata.SourceCatalogOnly)
}

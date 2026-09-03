package sina

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
		cfg.BaseURL = "https://finance.sina.com.cn"
	}
	return indexsource.NewHTTPProvider("sina", "index_http", "Sina Index", cfg.BaseURL, "finance.sina.com.cn", indexsource.RawCode, cfg.HTTPClient, cfg.Now, marketdata.SourceCatalogOnly)
}

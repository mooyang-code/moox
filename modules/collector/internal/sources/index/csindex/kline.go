package csindex

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
		cfg.BaseURL = "https://www.csindex.com.cn"
	}
	return indexsource.NewHTTPProvider("csindex", "index_http", "CSIndex", cfg.BaseURL, "www.csindex.com.cn", indexsource.RawCode, cfg.HTTPClient, cfg.Now, marketdata.SourceCatalogOnly)
}

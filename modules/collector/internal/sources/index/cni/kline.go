package cni

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
		cfg.BaseURL = "https://www.cnindex.com.cn"
	}
	return indexsource.NewHTTPProvider("cni", "index_cni_http", "CNI Index", cfg.BaseURL, "www.cnindex.com.cn", indexsource.RawCode, cfg.HTTPClient, cfg.Now, marketdata.SourceCatalogOnly)
}

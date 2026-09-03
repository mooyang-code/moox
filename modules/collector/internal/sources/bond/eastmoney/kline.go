package eastmoney

import (
	"net/http"
	"strings"
	"time"

	bondsource "github.com/mooyang-code/moox/modules/collector/internal/sources/bond"
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
	return bondsource.NewHTTPProvider("eastmoney", "convertible_bond_http", "EastMoney Convertible Bond", cfg.BaseURL, "push2.eastmoney.com", bondsource.EastMoneySecID, cfg.HTTPClient, cfg.Now)
}

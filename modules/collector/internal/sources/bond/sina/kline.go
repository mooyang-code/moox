package sina

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
		cfg.BaseURL = "https://quotes.sina.cn"
	}
	return bondsource.NewHTTPProvider("sina", "convertible_bond_http", "Sina Convertible Bond", cfg.BaseURL, "quotes.sina.cn", bondsource.SinaSymbol, cfg.HTTPClient, cfg.Now)
}

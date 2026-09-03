package tdx

import (
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	stocktdx "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn/tdx"
)

type Config struct {
	Host    string
	Port    int
	Timeout time.Duration
}

func New(cfg Config) *stocktdx.Provider {
	return stocktdx.New(stocktdx.Config{
		Host: cfg.Host, Port: cfg.Port, Timeout: cfg.Timeout,
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 1, Burst: 1, MaxConcurrent: 1,
			Cooldown: time.Second, RequestTimeout: cfg.Timeout,
		},
	})
}

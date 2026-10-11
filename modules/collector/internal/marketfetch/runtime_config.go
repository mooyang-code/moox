package marketfetch

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// The composition root consumes the same validated route configuration as the
// assignment planner, without placing concrete provider constructors here.
type StockCNRoute = stockCNRoute
type StockCNProviderRuntime = stockCNProviderRuntime
type StockCNProviderKlineConfig = stockCNProviderKlineFile

const KlineProviderAttemptBudget = klineProviderAttemptBudget

// RuntimeKlineProviderAttemptBudget reads the SCF-owned HTTP attempt budget.
// The static constant remains the local fallback and the retry cursor unit.
func RuntimeKlineProviderAttemptBudget() int {
	raw := strings.TrimSpace(os.Getenv("MOOX_FETCH_HTTP_MAX_ATTEMPTS"))
	if raw == "" {
		return klineProviderAttemptBudget
	}
	attempts, err := strconv.Atoi(raw)
	if err != nil || attempts <= 0 {
		return klineProviderAttemptBudget
	}
	return attempts
}

func LoadStockCNRoute() (StockCNRoute, error) { return loadStockCNRoute() }

func LoadStockCNProviderRuntime(route StockCNRoute) (map[string]StockCNProviderRuntime, error) {
	return loadStockCNProviderRuntime(route)
}

// attemptTimeoutFloor 是单次行情请求的最短超时，来自计划下发的 MOOX_FETCH_REQUEST_TIMEOUT_MS；未配置时为 0（不设下限）。
func attemptTimeoutFloor() time.Duration {
	millis := envInt("MOOX_FETCH_REQUEST_TIMEOUT_MS", 0)
	if millis <= 0 {
		return 0
	}
	return time.Duration(millis) * time.Millisecond
}

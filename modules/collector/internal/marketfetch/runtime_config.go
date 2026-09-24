package marketfetch

import (
	"os"
	"strconv"
	"strings"
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

func InstrumentRouteID(marketID, instrumentType string) string {
	return instrumentRouteID(marketID, instrumentType)
}

package marketfetch

// The composition root consumes the same validated route configuration as the
// assignment planner, without placing concrete provider constructors here.
type StockCNRoute = stockCNRoute
type StockCNProviderRuntime = stockCNProviderRuntime
type StockCNProviderKlineConfig = stockCNProviderKlineFile

const KlineProviderAttemptBudget = klineProviderAttemptBudget

func LoadStockCNRoute() (StockCNRoute, error) { return loadStockCNRoute() }

func LoadStockCNProviderRuntime(route StockCNRoute) (map[string]StockCNProviderRuntime, error) {
	return loadStockCNProviderRuntime(route)
}

func InstrumentRouteID(marketID, instrumentType string) string {
	return instrumentRouteID(marketID, instrumentType)
}

package config

// MarketFetchBudgetMS is the worst-case request and post-fetch budget. Storage
// is one shared write window, not one timeout per destination Dataset.
func MarketFetchBudgetMS(items, inflight, providers, attempts, requestTimeoutMS, storageTimeoutMS int, timer bool) int {
	waves := (items + inflight - 1) / inflight
	budget := waves*providers*attempts*requestTimeoutMS + storageTimeoutMS + SCFInstrumentNamesReserveMilliseconds + SCFColdCompletionReserveMilliseconds + SCFCLSReserveMilliseconds + SCFMetricsResponseReserveMilliseconds + SCFFinalResponseReserveMilliseconds
	if timer {
		budget += SCFTimerClaimReserveMilliseconds
	}
	return budget
}

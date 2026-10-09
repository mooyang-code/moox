package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestStockInvokeBudgetMatchesActiveRoute(t *testing.T) {
	data, err := os.ReadFile("../../../../collector/config/markets/stockcn/route.yaml")
	require.NoError(t, err)
	var route struct {
		Providers []struct {
			Kline string `yaml:"kline"`
		} `yaml:"providers"`
	}
	require.NoError(t, yaml.Unmarshal(data, &route))
	active := 0
	for _, provider := range route.Providers {
		if provider.Kline == "active" {
			active++
		}
	}
	require.Equal(t, active, StockCNInvokeProviderChainLength)
}

func TestStockSCFBudgetUsesIndependentTimerAndInvokeChains(t *testing.T) {
	base := SCFFetcherSpace{SpaceID: "stockcn", MemorySize: 64, TimeoutSeconds: 60,
		TimerFunctionCount: 1, MeasuredSafeGroupSize: 40,
		AccessAddress: "storage.example:11004", RealtimeBatchSize: 30,
		MaxInflightRequests: 10, RequestTimeoutMS: 1000, HTTPMaxAttempts: 4, Regions: []SCFFetcherRegion{{Region: "ap-singapore", Enabled: true, FunctionCount: 1, CloudAccountID: "sg"}}}
	require.NoError(t, validateSCFFetcherSpace(&base, "stock"))
	require.GreaterOrEqual(t, base.InvokeTimeoutSeconds, 90)
	require.GreaterOrEqual(t, SCFColdCompletionReserveMilliseconds, 13000)
	base.InvokeTimeoutSeconds = 90
	base.RequestTimeoutMS = 2000
	// Invoke has three waves, four active providers and four attempts: 96s
	// before Storage and publication. Timer has four waves of one bound source.
	require.ErrorContains(t, validateSCFFetcherSpace(&base, "stock"), "Invoke")
	base.InvokeTimeoutSeconds = 120
	require.NoError(t, validateSCFFetcherSpace(&base, "stock"))
	base.MaxInflightRequests = 1
	base.RealtimeBatchSize = 1
	base.InvokeTimeoutSeconds = 900
	require.ErrorContains(t, validateSCFFetcherSpace(&base, "stock"), "Timer")
}

func TestMarketFetchBudgetIncludesEveryReserveAndWholeWaves(t *testing.T) {
	require.Equal(t, 70500, MarketFetchBudgetMS(30, 10, 4, 4, 1000, 5000, false))
	require.Equal(t, 41500, MarketFetchBudgetMS(40, 10, 1, 4, 1000, 5000, true))
	require.Equal(t, 41500, MarketFetchBudgetMS(31, 10, 1, 4, 1000, 5000, true))
	require.Equal(t, 37500, MarketFetchBudgetMS(30, 10, 1, 4, 1000, 5000, true))
}

func TestStockTimerBudgetIncludesBestEffortNameReadAtTimeoutBoundary(t *testing.T) {
	// Forty bound-source items need four waves of four attempts. The read
	// reserve moves the maximum safe millisecond request timeout to 2156.
	require.Equal(t, 59996, MarketFetchBudgetMS(40, 10, 1, 4, 2156, 5000, true))
	require.Equal(t, 60012, MarketFetchBudgetMS(40, 10, 1, 4, 2157, 5000, true))
	space := SCFFetcherSpace{SpaceID: "stockcn", MemorySize: 64, TimeoutSeconds: 60, InvokeTimeoutSeconds: 200,
		TimerFunctionCount: 1, MeasuredSafeGroupSize: 40, AccessAddress: "storage.example:11004",
		RealtimeBatchSize: 30, MaxInflightRequests: 10, RequestTimeoutMS: 2156, HTTPMaxAttempts: 4, Regions: []SCFFetcherRegion{{Region: "ap-singapore", Enabled: true, FunctionCount: 1, CloudAccountID: "sg"}}}
	require.NoError(t, validateSCFFetcherSpace(&space, "stock"))
	space.RequestTimeoutMS++
	require.ErrorContains(t, validateSCFFetcherSpace(&space, "stock"), "Timer")
}

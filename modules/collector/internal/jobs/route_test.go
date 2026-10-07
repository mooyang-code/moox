package jobs

import "testing"

func TestJobRouteForNormalizesExchangeAndDataType(t *testing.T) {
	route, ok := JobRouteFor(" Binance ", " KLINE ")
	if !ok {
		t.Fatal("JobRouteFor() did not find Binance K-line route")
	}
	if route.Exchange != "binance" || route.DataType != "kline" ||
		route.JobType != JobTypeCollectBinanceKline {
		t.Fatalf("route = %#v", route)
	}
}

func TestJobRoutesRejectDuplicateIdentity(t *testing.T) {
	routes := []JobRoute{
		{Exchange: "binance", DataType: "kline", JobType: "collect.binance.kline"},
		{Exchange: "BINANCE", DataType: " KLINE ", JobType: "collect.other.kline"},
	}
	if err := validateJobRoutes(routes); err == nil {
		t.Fatal("validateJobRoutes() accepted duplicate exchange/data_type")
	}

	routes = []JobRoute{
		{Exchange: "binance", DataType: "kline", JobType: "collect.binance.kline"},
		{Exchange: "eastmoney", DataType: "kline", JobType: " collect.binance.kline "},
	}
	if err := validateJobRoutes(routes); err == nil {
		t.Fatal("validateJobRoutes() accepted duplicate job_type")
	}
}

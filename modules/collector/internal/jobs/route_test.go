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

func TestJobRouteByJobTypeTrimsButRequiresExactCase(t *testing.T) {
	route, ok := JobRouteByJobType(" collect.binance.symbol ")
	if !ok || route.JobType != JobTypeCollectBinanceSymbol {
		t.Fatalf("route = %#v, ok = %v", route, ok)
	}
	if _, ok := JobRouteByJobType("COLLECT.BINANCE.SYMBOL"); ok {
		t.Fatal("JobRouteByJobType() accepted a case variant")
	}
	if _, ok := JobRouteByJobType("collect.symbol"); ok {
		t.Fatal("JobRouteByJobType() retained obsolete generic alias")
	}
}

func TestSupportedJobTypesReturnsProviderSpecificStableCopy(t *testing.T) {
	got := SupportedJobTypes()
	if len(got) != 2 ||
		got[0] != JobTypeCollectBinanceKline ||
		got[1] != JobTypeCollectBinanceSymbol {
		t.Fatalf("SupportedJobTypes() = %#v", got)
	}
	got[0] = "modified"
	if next := SupportedJobTypes(); next[0] != JobTypeCollectBinanceKline {
		t.Fatalf("SupportedJobTypes() returned shared storage: %#v", next)
	}
}

func TestValidateJobTypesRejectsUnknownRoute(t *testing.T) {
	if err := ValidateJobTypes([]string{JobTypeCollectBinanceKline}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateJobTypes([]string{"collect.kline"}); err == nil {
		t.Fatal("ValidateJobTypes() accepted legacy job type")
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

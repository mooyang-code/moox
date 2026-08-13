package rpc

import "testing"

func TestServiceNames(t *testing.T) {
	if ExchangeAccountServiceName != "trpc.moox.trade.ExchangeAccountService" {
		t.Fatalf("account service name = %q", ExchangeAccountServiceName)
	}
	if LogicalAccountServiceName != "trpc.moox.trade.LogicalAccountService" {
		t.Fatalf("logical account service name = %q", LogicalAccountServiceName)
	}
	if TradeExecutionServiceName != "trpc.moox.trade.TradeExecutionService" {
		t.Fatalf("execution service name = %q", TradeExecutionServiceName)
	}
	if TradeDNSResolverServiceName != "trpc.moox.trade.TradeDNSResolverService" {
		t.Fatalf("dns resolver service name = %q", TradeDNSResolverServiceName)
	}
	if TradeDNSResolverTRPCName != "trpc.moox.trade.TradeDNSResolverService.trpc" {
		t.Fatalf("dns resolver trpc service name = %q", TradeDNSResolverTRPCName)
	}
}

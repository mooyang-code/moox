package rpc

import "testing"

func TestServiceNames(t *testing.T) {
	if TradeConsoleServiceName != "trpc.moox.trade.TradeConsoleService" {
		t.Fatalf("console service name = %q", TradeConsoleServiceName)
	}
	if TradeDNSResolverServiceName != "trpc.moox.trade.TradeDNSResolverService" {
		t.Fatalf("dns resolver service name = %q", TradeDNSResolverServiceName)
	}
	if TradeDNSResolverTRPCName != "trpc.moox.trade.TradeDNSResolverService.trpc" {
		t.Fatalf("dns resolver trpc service name = %q", TradeDNSResolverTRPCName)
	}
}

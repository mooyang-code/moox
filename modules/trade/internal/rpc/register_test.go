package rpc

import (
	"strings"
	"testing"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

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

func TestTradeConsoleAdminAliasDescriptorUsesDistinctPath(t *testing.T) {
	desc := tradeConsoleAdminServiceDesc()
	if desc.ServiceName != TradeConsoleAdminServiceName {
		t.Fatalf("alias service name = %q", desc.ServiceName)
	}
	if len(desc.Methods) != len(tradepb.TradeConsoleServiceServer_ServiceDesc.Methods) {
		t.Fatalf("alias methods = %d, want %d", len(desc.Methods), len(tradepb.TradeConsoleServiceServer_ServiceDesc.Methods))
	}
	for _, method := range desc.Methods {
		if !strings.HasPrefix(method.Name, "/"+TradeConsoleAdminServiceName+"/") {
			t.Fatalf("alias method path = %q", method.Name)
		}
	}
	if tradepb.TradeConsoleServiceServer_ServiceDesc.ServiceName != TradeConsoleServiceName {
		t.Fatal("alias construction mutated canonical descriptor")
	}
}

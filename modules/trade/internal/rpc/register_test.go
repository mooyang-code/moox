package rpc

import "testing"

func TestServiceNames(t *testing.T) {
	if ExchangeAccountServiceName != "trpc.moox.trade.ExchangeAccountService" {
		t.Fatalf("account service name = %q", ExchangeAccountServiceName)
	}
	if TradeExecutionServiceName != "trpc.moox.trade.TradeExecutionService" {
		t.Fatalf("execution service name = %q", TradeExecutionServiceName)
	}
}

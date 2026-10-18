package rpc

import (
	"testing"
)

func TestServiceNames(t *testing.T) {
	if TradeConsoleServiceName != "trpc.moox.trade.TradeConsoleService" {
		t.Fatalf("console service name = %q", TradeConsoleServiceName)
	}
}

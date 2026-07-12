package rpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceNameConstants_ShouldMatchTRPCNames(t *testing.T) {
	assert.Equal(t, "trpc.moox.trade.AccountSvc", AccountSvcName)
	assert.Equal(t, "trpc.moox.trade.TradeOpsSvc", TradeOpsSvcName)
	assert.Equal(t, "trpc.moox.trade.RebalanceSvc", RebalanceSvcName)
}

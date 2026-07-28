package rpc

import (
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	ExchangeAccountServiceName = "trpc.moox.trade.ExchangeAccountService"
	TradeExecutionServiceName  = "trpc.moox.trade.TradeExecutionService"
)

func RegisterAll(
	s *server.Server,
	accounts *AccountServer,
	execution *ExecutionServer,
) {
	tradepb.RegisterExchangeAccountServiceService(
		s.Service(ExchangeAccountServiceName),
		accounts,
	)
	tradepb.RegisterTradeExecutionServiceService(
		s.Service(TradeExecutionServiceName),
		execution,
	)
}

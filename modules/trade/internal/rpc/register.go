package rpc

import (
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	ExchangeAccountServiceName = "trpc.moox.trade.ExchangeAccountService"
	LogicalAccountServiceName  = "trpc.moox.trade.LogicalAccountService"
	TradeExecutionServiceName  = "trpc.moox.trade.TradeExecutionService"
)

func RegisterAll(
	s *server.Server,
	accounts *AccountServer,
	logicalAccounts *LogicalAccountServer,
	execution *ExecutionServer,
) {
	tradepb.RegisterExchangeAccountServiceService(
		s.Service(ExchangeAccountServiceName),
		accounts,
	)
	tradepb.RegisterLogicalAccountServiceService(
		s.Service(LogicalAccountServiceName),
		logicalAccounts,
	)
	tradepb.RegisterTradeExecutionServiceService(
		s.Service(TradeExecutionServiceName),
		execution,
	)
}

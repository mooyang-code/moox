package rpc

import (
	papersimulation "github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/server"
)

const TradeConsoleServiceName = "trpc.moox.trade.TradeConsoleService"

func RegisterAll(
	s *server.Server,
	accounts *AccountServer,
	logicalAccounts *LogicalAccountServer,
	execution *ExecutionServer,
	options ...ConsoleOptions,
) {
	consoleService := s.Service(TradeConsoleServiceName)
	if consoleService == nil {
		panic("TradeConsoleService is not configured")
	}
	console := &ConsoleServer{
		AccountServer: accounts, LogicalAccountServer: logicalAccounts,
		ExecutionServer: execution, Store: accounts.Store,
	}
	if len(options) > 0 {
		console.Paper = options[0].Paper
		console.LiveTradingEnabled = options[0].LiveTradingEnabled
		console.MatcherReady = options[0].MatcherReady
		console.Holdings = options[0].Holdings
	}
	tradepb.RegisterTradeConsoleServiceService(consoleService, console)
}

type ConsoleOptions struct {
	Paper              *papersimulation.Service
	LiveTradingEnabled bool
	MatcherReady       func() bool
	Holdings           HoldingQueryService
}

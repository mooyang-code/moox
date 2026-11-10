package rpc

import (
	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	papersimulation "github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	executionport "github.com/mooyang-code/moox/modules/trade/internal/execution"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/server"
)

const TradeConsoleServiceName = "trpc.moox.trade.TradeConsoleService"

func RegisterAll(
	s *server.Server,
	accounts *AccountServer,
	logicalAccounts *LogicalAccountServer,
	execution *ExecutionServer,
	options ConsoleOptions,
) {
	consoleService := s.Service(TradeConsoleServiceName)
	if consoleService == nil {
		panic("TradeConsoleService is not configured")
	}
	console := &ConsoleServer{
		AccountServer: accounts, LogicalAccountServer: logicalAccounts,
		ExecutionServer: execution, Store: accounts.Store,
	}
	console.Paper = options.Paper
	console.LiveTradingEnabled = options.LiveTradingEnabled
	console.MatcherReady = options.MatcherReady
	console.Holdings = options.Holdings
	console.Exchanges = options.Exchanges
	console.Adapters = options.Adapters
	console.FlattenAccount = options.FlattenAccount
	console.Delist = options.Delist
	console.FlattenInstrumentAction = options.FlattenInstrument
	tradepb.RegisterTradeConsoleServiceService(consoleService, console)
}

type ConsoleOptions struct {
	Paper              *papersimulation.Service
	LiveTradingEnabled bool
	MatcherReady       func() bool
	Holdings           HoldingQueryService
	Exchanges          executionport.ExchangeCatalog
	Adapters           AdapterSource
	FlattenAccount     FlattenTradingAccountFunc
	Delist             *delistapp.Service
	FlattenInstrument  FlattenInstrumentFunc
}

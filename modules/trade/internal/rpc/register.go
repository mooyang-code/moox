package rpc

import (
	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	executionport "github.com/mooyang-code/moox/modules/trade/internal/execution"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/lifecycle"
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
	if accounts != nil && accounts.Logical == nil {
		accounts.Logical = logicalAccounts
	}
	console := &ConsoleServer{
		AccountServer: accounts, LogicalAccountServer: logicalAccounts,
		ExecutionServer: execution, Store: accounts.Store,
	}
	console.LiveTradingEnabled = options.LiveTradingEnabled
	console.MatcherReady = options.MatcherReady
	console.Holdings = options.Holdings
	console.Exchanges = options.Exchanges
	console.Adapters = options.Adapters
	console.FlattenAccount = options.FlattenAccount
	console.Delist = options.Delist
	console.Lifecycle = options.Lifecycle
	tradepb.RegisterTradeConsoleServiceService(consoleService, console)
}

type ConsoleOptions struct {
	LiveTradingEnabled bool
	MatcherReady       func() bool
	Holdings           HoldingQueryService
	Exchanges          executionport.ExchangeCatalog
	Adapters           AdapterSource
	FlattenAccount     FlattenTradingAccountFunc
	Delist             *delistapp.Service
	Lifecycle          *lifecycle.Controller
}

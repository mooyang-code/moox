package rpc

import (
	papersimulation "github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	TradeConsoleServiceName     = "trpc.moox.trade.TradeConsoleService"
	TradeDNSResolverServiceName = "trpc.moox.trade.TradeDNSResolverService"
	TradeDNSResolverTRPCName    = TradeDNSResolverServiceName + ".trpc"
)

func RegisterAll(
	s *server.Server,
	accounts *AccountServer,
	logicalAccounts *LogicalAccountServer,
	execution *ExecutionServer,
	dnsResolver *DNSResolverServer,
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
	dnsRegistered := false
	for _, name := range []string{TradeDNSResolverTRPCName, TradeDNSResolverServiceName} {
		if service := s.Service(name); service != nil {
			tradepb.RegisterTradeDNSResolverServiceService(service, dnsResolver)
			dnsRegistered = true
		}
	}
	if !dnsRegistered {
		panic("TradeDNSResolverService is not configured")
	}
}

type ConsoleOptions struct {
	Paper              *papersimulation.Service
	LiveTradingEnabled bool
	MatcherReady       func() bool
	Holdings           HoldingQueryService
}

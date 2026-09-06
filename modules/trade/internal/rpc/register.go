package rpc

import (
	"strings"

	papersimulation "github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	TradeConsoleServiceName = "trpc.moox.trade.TradeConsoleService"
	// TradeConsoleAdminServiceName is a path alias used by a dedicated Trade
	// node for the browser/operator route. Keeping it distinct from the
	// strategy-owned canonical service path lets old gateways roll back without
	// ambiguous native route selection.
	TradeConsoleAdminServiceName = "trpc.moox.trade.TradeConsoleAdminService"
	TradeDNSResolverServiceName  = "trpc.moox.trade.TradeDNSResolverService"
	TradeDNSResolverTRPCName     = TradeDNSResolverServiceName + ".trpc"
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
	registerTradeConsoleAdminAlias(consoleService, console)
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

func registerTradeConsoleAdminAlias(service server.Service, console tradepb.TradeConsoleServiceService) {
	if err := service.Register(tradeConsoleAdminServiceDesc(), console); err != nil {
		panic("TradeConsoleAdminService register error: " + err.Error())
	}
}

func tradeConsoleAdminServiceDesc() *server.ServiceDesc {
	desc := tradepb.TradeConsoleServiceServer_ServiceDesc
	const canonicalPrefix = "/" + TradeConsoleServiceName + "/"
	const aliasPrefix = "/" + TradeConsoleAdminServiceName + "/"
	desc.ServiceName = TradeConsoleAdminServiceName
	desc.Methods = append([]server.Method(nil), desc.Methods...)
	for i := range desc.Methods {
		desc.Methods[i].Name = strings.Replace(desc.Methods[i].Name, canonicalPrefix, aliasPrefix, 1)
	}
	return &desc
}

type ConsoleOptions struct {
	Paper              *papersimulation.Service
	LiveTradingEnabled bool
	MatcherReady       func() bool
	Holdings           HoldingQueryService
}

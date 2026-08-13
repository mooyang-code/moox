package rpc

import (
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	ExchangeAccountServiceName  = "trpc.moox.trade.ExchangeAccountService"
	LogicalAccountServiceName   = "trpc.moox.trade.LogicalAccountService"
	TradeExecutionServiceName   = "trpc.moox.trade.TradeExecutionService"
	TradeDNSResolverServiceName = "trpc.moox.trade.TradeDNSResolverService"
	TradeDNSResolverTRPCName    = TradeDNSResolverServiceName + ".trpc"
)

func RegisterAll(
	s *server.Server,
	accounts *AccountServer,
	logicalAccounts *LogicalAccountServer,
	execution *ExecutionServer,
	dnsResolver *DNSResolverServer,
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

package rpc

import (
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"

	"trpc.group/trpc-go/trpc-go/server"
)

// 服务名常量（与 config/trpc_go.yaml 一致）。
const (
	AccountSvcName    = "trpc.moox.trade.AccountSvc"
	BalanceSvcName    = "trpc.moox.trade.BalanceSvc"
	FundSvcName       = "trpc.moox.trade.FundSvc"
	ApiKeySvcName     = "trpc.moox.trade.ApiKeySvc"
	ChannelSvcName    = "trpc.moox.trade.ChannelSvc"
	TradeOpSvcName    = "trpc.moox.trade.TradeOpSvc"
	OrderSvcName      = "trpc.moox.trade.OrderSvc"
	TradeQuerySvcName = "trpc.moox.trade.TradeQuerySvc"
	PositionSvcName   = "trpc.moox.trade.PositionSvc"
)

// RegisterAll 把 9 个 service 注册到 trpc server。
func RegisterAll(s *server.Server, svc *service.Service) {
	h := New(svc)
	mooxpb.RegisterAccountSvcService(s.Service(AccountSvcName), h)
	mooxpb.RegisterBalanceSvcService(s.Service(BalanceSvcName), h)
	mooxpb.RegisterFundSvcService(s.Service(FundSvcName), h)
	mooxpb.RegisterApiKeySvcService(s.Service(ApiKeySvcName), h)
	mooxpb.RegisterChannelSvcService(s.Service(ChannelSvcName), h)
	mooxpb.RegisterTradeOpSvcService(s.Service(TradeOpSvcName), h)
	mooxpb.RegisterOrderSvcService(s.Service(OrderSvcName), h)
	mooxpb.RegisterTradeQuerySvcService(s.Service(TradeQuerySvcName), h)
	mooxpb.RegisterPositionSvcService(s.Service(PositionSvcName), h)
}

package rpc

import (
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"

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
	RebalanceSvcName  = "trpc.moox.trade.RebalanceSvc"
	TradeOpsSvcName   = "trpc.moox.trade.TradeOpsSvc"
)

// RegisterAll 把 9 个 service 注册到 trpc server。
func RegisterAll(s *server.Server, svc *service.Service, kernel ...*command.Engine) {
	h := New(svc, kernel...)
	tradepb.RegisterAccountSvcService(s.Service(AccountSvcName), h)
	tradepb.RegisterBalanceSvcService(s.Service(BalanceSvcName), h)
	tradepb.RegisterFundSvcService(s.Service(FundSvcName), h)
	tradepb.RegisterApiKeySvcService(s.Service(ApiKeySvcName), h)
	tradepb.RegisterChannelSvcService(s.Service(ChannelSvcName), h)
	tradepb.RegisterTradeOpSvcService(s.Service(TradeOpSvcName), h)
	tradepb.RegisterOrderSvcService(s.Service(OrderSvcName), h)
	tradepb.RegisterTradeQuerySvcService(s.Service(TradeQuerySvcName), h)
	tradepb.RegisterPositionSvcService(s.Service(PositionSvcName), h)
	tradepb.RegisterRebalanceSvcService(s.Service(RebalanceSvcName), h)
	tradepb.RegisterTradeOpsSvcService(s.Service(TradeOpsSvcName), h)
}

package rpc

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/delist"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/events"
	"trpc.group/trpc-go/trpc-go/log"
)

// DelistRegistrar 登记一条下架通知（delist.Registrar 实现它）。
type DelistRegistrar interface {
	Register(ctx context.Context, notice delist.Notice) (created bool, eventID string, err error)
}

// SetDelistRegistrar 接上下架登记；启动时调用一次。没有配置事件总线时不接，手工登记返回明确的错误。
func (s *Service) SetDelistRegistrar(registrar DelistRegistrar) { s.delistRegistrar = registrar }

// RegisterDelistNotice 手工登记一条标的下架通知，用于补漏报和演练；可以登记过去的下架时间。
func (s *Service) RegisterDelistNotice(ctx context.Context, req *pb.RegisterDelistNoticeReq) (*pb.RegisterDelistNoticeRsp, error) {
	if s.delistRegistrar == nil {
		return &pb.RegisterDelistNoticeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "没有配置 EventBus，不能登记下架通知（Collector 配置里的 event_bus）")}, nil
	}
	scope := events.DelistScopeAll
	if req.GetPaperOnly() {
		scope = events.DelistScopePaperOnly
	}
	notice := delist.Notice{
		Exchange: strings.TrimSpace(req.GetExchange()), Market: strings.TrimSpace(req.GetMarket()), Symbol: strings.ToUpper(strings.TrimSpace(req.GetExchangeSymbol())),
		DelistAt: time.UnixMilli(req.GetDelistAtMs()).UTC(), Source: events.DelistSourceManual, Scope: scope,
	}
	if req.GetDelistAtMs() <= 0 {
		return &pb.RegisterDelistNoticeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "下架时间不能为空")}, nil
	}
	if err := notice.Validate(); err != nil {
		return &pb.RegisterDelistNoticeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	created, eventID, err := s.delistRegistrar.Register(ctx, notice)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] 手工登记下架通知失败 %s:%s:%s: %v", notice.Exchange, notice.Market, notice.Symbol, err)
		return &pb.RegisterDelistNoticeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "登记下架通知失败，请查看 Collector 日志")}, nil
	}
	return &pb.RegisterDelistNoticeRsp{RetInfo: retOK(), Created: created, EventId: eventID}, nil
}

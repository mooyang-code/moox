package rpc

import (
	"context"

	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

func delistKey(exchange tradepb.Exchange, market tradepb.MarketType, symbol string) delistapp.Key {
	return delistapp.Key{
		Exchange:       string(exchangeFromPB(exchange)),
		MarketType:     string(marketFromPB(market)),
		ExchangeSymbol: symbol,
	}
}

// UnblockInstrument 解除禁止，并取消这个标的进行中的下架处理（含演练）。全局方法，不检查空间。
func (s *ConsoleServer) UnblockInstrument(ctx context.Context, req *tradepb.UnblockInstrumentReq) (*tradepb.UnblockInstrumentRsp, error) {
	if err := validatePB(req); err != nil {
		return &tradepb.UnblockInstrumentRsp{RetInfo: invalidInfo(err)}, nil
	}
	if s.Delist == nil {
		return &tradepb.UnblockInstrumentRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	removed, err := s.Delist.Unblock(ctx, delistKey(req.GetExchange(), req.GetMarketType(), req.GetExchangeSymbol()))
	return &tradepb.UnblockInstrumentRsp{RetInfo: errorInfo(err), Removed: removed}, nil
}

// ListBlockedInstruments 列出下架名单。全局方法，不检查空间。
func (s *ConsoleServer) ListBlockedInstruments(ctx context.Context, req *tradepb.ListBlockedInstrumentsReq) (*tradepb.ListBlockedInstrumentsRsp, error) {
	if s.Delist == nil {
		return &tradepb.ListBlockedInstrumentsRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	records, err := s.Delist.List(ctx)
	if err != nil {
		return &tradepb.ListBlockedInstrumentsRsp{RetInfo: errorInfo(err)}, nil
	}
	rsp := &tradepb.ListBlockedInstrumentsRsp{RetInfo: success(), Instruments: make([]*tradepb.BlockedInstrument, 0, len(records))}
	for _, record := range records {
		rsp.Instruments = append(rsp.Instruments, &tradepb.BlockedInstrument{
			Exchange: exchangeToPB(record.Exchange), MarketType: marketToPB(record.MarketType), ExchangeSymbol: record.ExchangeSymbol,
			InstrumentId: record.InstrumentID, BaseAsset: record.BaseAsset, Reason: record.Reason,
			BlockedAt: record.BlockedAt, Scope: record.Scope, DelistId: record.DelistID,
		})
	}
	return rsp, nil
}

// ListInstrumentHolders 跨空间列出策略持有该标的的执行账户。全局方法，不检查空间；
// 没有持有者时 holders 是空数组（经网关的 JSON 序列化不会省略它）。
func (s *ConsoleServer) ListInstrumentHolders(ctx context.Context, req *tradepb.ListInstrumentHoldersReq) (*tradepb.ListInstrumentHoldersRsp, error) {
	if err := validatePB(req); err != nil {
		return &tradepb.ListInstrumentHoldersRsp{RetInfo: invalidInfo(err)}, nil
	}
	if s.Delist == nil {
		return &tradepb.ListInstrumentHoldersRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	result, err := s.Delist.Holders(ctx, delistKey(req.GetExchange(), req.GetMarketType(), req.GetExchangeSymbol()), req.GetPaperOnly())
	if err != nil {
		return &tradepb.ListInstrumentHoldersRsp{RetInfo: errorInfo(err)}, nil
	}
	rsp := &tradepb.ListInstrumentHoldersRsp{
		RetInfo: success(), InstrumentUnknown: result.InstrumentUnknown,
		Holders: make([]*tradepb.InstrumentHolder, 0, len(result.Holders)),
	}
	for _, holder := range result.Holders {
		rsp.Holders = append(rsp.Holders, &tradepb.InstrumentHolder{
			SpaceId: holder.SpaceID, LogicalAccountId: holder.LogicalAccountID, TradingAccountId: holder.TradingAccountID,
			Quantity: holder.Quantity, Dust: holder.Dust, Paper: holder.Paper, Status: holder.Status, Note: holder.Note,
		})
	}
	for _, skipped := range result.SkippedAccounts {
		rsp.SkippedAccounts = append(rsp.SkippedAccounts, &tradepb.SkippedAccount{TradingAccountId: skipped.TradingAccountID, Reason: skipped.Reason})
	}
	rsp.Notes = result.Notes
	return rsp, nil
}

// ListDelists 列出下架记录（按创建时间倒序），带各轮各组合的动作状态。全局方法，不检查空间。
func (s *ConsoleServer) ListDelists(ctx context.Context, req *tradepb.ListDelistsReq) (*tradepb.ListDelistsRsp, error) {
	if err := validatePB(req); err != nil {
		return &tradepb.ListDelistsRsp{RetInfo: invalidInfo(err)}, nil
	}
	if s.Delist == nil {
		return &tradepb.ListDelistsRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	views, err := s.Delist.ListDelists(ctx, req.GetActiveOnly(), int(req.GetLimit()))
	if err != nil {
		return &tradepb.ListDelistsRsp{RetInfo: errorInfo(err)}, nil
	}
	rsp := &tradepb.ListDelistsRsp{RetInfo: success(), Delists: make([]*tradepb.Delist, 0, len(views))}
	for _, view := range views {
		delist := &tradepb.Delist{
			DelistId: view.DelistID, Exchange: exchangeToPB(view.Exchange), MarketType: marketToPB(view.MarketType),
			ExchangeSymbol: view.ExchangeSymbol, Scope: view.Scope, DelistAtMs: view.DelistAt,
			NoticeSource: view.Source, SourceEventId: view.SourceEventID, Status: view.Status, PlannedRound: int32(view.PlannedRound),
			Note: view.Note, Result: view.Result, CreatedAtMs: view.CreatedAt, FinishedAtMs: view.FinishedAt,
			Flattens: make([]*tradepb.DelistFlatten, 0, len(view.Flattens)),
		}
		for _, flatten := range view.Flattens {
			delist.Flattens = append(delist.Flattens, &tradepb.DelistFlatten{
				Round: int32(flatten.Round), SpaceId: flatten.SpaceID, LogicalAccountId: flatten.LogicalAccountID,
				ActionId: flatten.ActionID, ActionStatus: flatten.ActionStatus, LastError: flatten.LastError,
				CreatedAtMs: flatten.CreatedAt,
			})
		}
		rsp.Delists = append(rsp.Delists, delist)
	}
	return rsp, nil
}

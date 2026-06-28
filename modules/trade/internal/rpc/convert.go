// Package rpc 实现 Trade 模块的 tRPC handler 层：PB <-> 领域模型转换 +
// 9 个 service 接口的实现，统一从 ctx 读取 space_id（由网关 authorize 注入）。
//
// 错误约定：service 层返回 ErrInvalidParam/ErrNotFound/ErrConflict/ErrInsufficient，
// 本层映射为 common.ErrorCode；其它错误统一 INNER_ERR。
package rpc

import (
	"context"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

// httpReq 从 ctx 取出底层 http 请求（可能为 nil）。
func httpReq(ctx context.Context) *http.Request {
	return thttp.Request(ctx)
}

// ===== space_id =====

func spaceID(ctx context.Context) string {
	sid, _ := spacecontext.FromContext(ctx)
	return sid
}

// userID 从请求头 X-User-Id 读取（由网关 authorize 注入）。可空。
func userID(ctx context.Context) string {
	if r := httpReq(ctx); r != nil {
		return r.Header.Get("X-User-Id")
	}
	return ""
}

// ===== RetInfo / Page =====

func retInfo(code mooxpb.ErrorCode, msg string) *mooxpb.RetInfo {
	return &mooxpb.RetInfo{Code: code, Msg: msg}
}

// errToRetInfo 把 service 错误映射为 ErrorCode。
func errToRetInfo(err error) *mooxpb.RetInfo {
	if err == nil {
		return retInfo(mooxpb.ErrorCode_SUCCESS, "")
	}
	switch err {
	case service.ErrInvalidParam:
		return retInfo(mooxpb.ErrorCode_INVALID_PARAM, err.Error())
	case service.ErrNotFound:
		return retInfo(mooxpb.ErrorCode_NOT_FOUND, err.Error())
	case service.ErrConflict, service.ErrInsufficient:
		return retInfo(mooxpb.ErrorCode_INVALID_PARAM, err.Error())
	default:
		return retInfo(mooxpb.ErrorCode_INNER_ERR, err.Error())
	}
}

func pageFromPB(p *mooxpb.Page) service.Page {
	if p == nil {
		return service.Page{}
	}
	return service.Page{PageNo: int(p.GetPage()), PageSize: int(p.GetSize())}
}

func pageResult(page service.Page, total int) *mooxpb.PageResult {
	return &mooxpb.PageResult{
		Page:    uint32(page.PageNo),
		Size:    uint32(page.PageSize),
		Total:   uint32(total),
		HasMore: page.PageNo*page.PageSize < total,
	}
}

// unixOrZero 返回 t 的秒级 epoch；零值返回 0。
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// ===== 枚举映射 =====

func accountTypeToDomain(t mooxpb.AccountType) service.AccountType {
	switch t {
	case mooxpb.AccountType_ACCOUNT_TYPE_MARGIN:
		return service.AccountMargin
	case mooxpb.AccountType_ACCOUNT_TYPE_SWAP:
		return service.AccountSwap
	case mooxpb.AccountType_ACCOUNT_TYPE_SIM:
		return service.AccountSim
	default:
		return service.AccountSpot
	}
}

func accountTypeToPB(t service.AccountType) mooxpb.AccountType {
	switch t {
	case service.AccountMargin:
		return mooxpb.AccountType_ACCOUNT_TYPE_MARGIN
	case service.AccountSwap:
		return mooxpb.AccountType_ACCOUNT_TYPE_SWAP
	case service.AccountSim:
		return mooxpb.AccountType_ACCOUNT_TYPE_SIM
	default:
		return mooxpb.AccountType_ACCOUNT_TYPE_SPOT
	}
}

func accountStatusToPB(s service.AccountStatus) mooxpb.AccountStatus {
	switch s {
	case service.AccountDisabled:
		return mooxpb.AccountStatus_ACCOUNT_STATUS_DISABLED
	case service.AccountFrozen:
		return mooxpb.AccountStatus_ACCOUNT_STATUS_FROZEN
	case service.AccountReadonly:
		return mooxpb.AccountStatus_ACCOUNT_STATUS_READONLY
	default:
		return mooxpb.AccountStatus_ACCOUNT_STATUS_NORMAL
	}
}

func marketTypeToDomain(m mooxpb.MarketType) string {
	switch m {
	case mooxpb.MarketType_MARKET_TYPE_MARGIN:
		return "margin"
	case mooxpb.MarketType_MARKET_TYPE_SWAP:
		return "swap"
	case mooxpb.MarketType_MARKET_TYPE_FUTURES:
		return "futures"
	default:
		return "spot"
	}
}

func marketTypeToPB(s string) mooxpb.MarketType {
	switch s {
	case "margin":
		return mooxpb.MarketType_MARKET_TYPE_MARGIN
	case "swap":
		return mooxpb.MarketType_MARKET_TYPE_SWAP
	case "futures":
		return mooxpb.MarketType_MARKET_TYPE_FUTURES
	default:
		return mooxpb.MarketType_MARKET_TYPE_SPOT
	}
}

func orderSideToDomain(s mooxpb.OrderSide) string {
	if s == mooxpb.OrderSide_ORDER_SIDE_SELL {
		return "sell"
	}
	return "buy"
}

func orderTypeToDomain(t mooxpb.OrderType) string {
	switch t {
	case mooxpb.OrderType_ORDER_TYPE_MARKET:
		return "market"
	case mooxpb.OrderType_ORDER_TYPE_STOP:
		return "stop"
	case mooxpb.OrderType_ORDER_TYPE_STOP_LIMIT:
		return "stop_limit"
	case mooxpb.OrderType_ORDER_TYPE_POST_ONLY:
		return "post_only"
	case mooxpb.OrderType_ORDER_TYPE_IOC:
		return "ioc"
	case mooxpb.OrderType_ORDER_TYPE_FOK:
		return "fok"
	default:
		return "limit"
	}
}

func orderStatusToPB(s int) mooxpb.OrderStatus { return mooxpb.OrderStatus(s) }

func channelStatusToPB(s int) mooxpb.ChannelStatus { return mooxpb.ChannelStatus(s) }

// ===== 模型 -> PB =====

func accountToPB(a *service.Account) *mooxpb.Account {
	if a == nil {
		return nil
	}
	return &mooxpb.Account{
		AccountId:    a.AccountID,
		UserId:       a.UserID,
		AccountName:  a.AccountName,
		AccountType:  accountTypeToPB(a.AccountType),
		ChannelId:    a.ChannelID,
		BaseCurrency: a.BaseCurrency,
		Status:       accountStatusToPB(a.Status),
		IsDefault:    a.IsDefault,
		Remark:       a.Remark,
		CreatedAt:    unixOrZero(a.CreatedAt),
		UpdatedAt:    unixOrZero(a.UpdatedAt),
	}
}

func balanceToPB(b *service.Balance) *mooxpb.Balance {
	if b == nil {
		return nil
	}
	return &mooxpb.Balance{
		AccountId: b.AccountID,
		Currency:  b.Currency,
		Available: b.Available,
		Frozen:    b.Frozen,
		Total:     b.Total,
	}
}

func fundFlowToPB(f *service.FundFlow) *mooxpb.FundFlow {
	if f == nil {
		return nil
	}
	return &mooxpb.FundFlow{
		FlowId:       f.FlowID,
		AccountId:    f.AccountID,
		Currency:     f.Currency,
		BizType:      f.BizType,
		Direction:    int32(f.Direction),
		Amount:       f.Amount,
		BalanceAfter: f.BalanceAfter,
		RefType:      f.RefType,
		RefId:        f.RefID,
		Remark:       f.Remark,
		CreatedAt:    unixOrZero(f.CreatedAt),
	}
}

func apiKeyToPB(k *service.APIKey) *mooxpb.ApiKey {
	if k == nil {
		return nil
	}
	return &mooxpb.ApiKey{
		ApiKeyId:    k.APIKeyID,
		AccountId:   k.AccountID,
		Exchange:    k.Exchange,
		ApiKey:      k.APIKey,
		Permissions: k.PermissionsRaw,
		Status:      int32(k.Status),
		CreatedAt:   unixOrZero(k.CreatedAt),
	}
}

func channelToPB(c *service.TradeChannel) *mooxpb.TradeChannel {
	if c == nil {
		return nil
	}
	return &mooxpb.TradeChannel{
		ChannelId:     c.ChannelID,
		ChannelName:   c.ChannelName,
		Exchange:      c.Exchange,
		MarketType:    marketTypeToPB(c.MarketType),
		AccountId:     c.AccountID,
		ApiKeyId:      c.APIKeyID,
		Endpoint:      c.Endpoint,
		IsSimulated:   c.IsSimulated,
		Status:        channelStatusToPB(c.Status),
		RateLimit:     int32(c.RateLimit),
		LastHeartbeat: unixOrZero(c.LastHeartbeat),
		CreatedAt:     unixOrZero(c.CreatedAt),
		UpdatedAt:     unixOrZero(c.UpdatedAt),
	}
}

func orderToPB(o *service.Order) *mooxpb.Order {
	if o == nil {
		return nil
	}
	return &mooxpb.Order{
		OrderId:         o.OrderID,
		ClientOrderId:   o.ClientOrderID,
		ExchangeOrderId: o.ExchangeOrderID,
		AccountId:       o.AccountID,
		ChannelId:       o.ChannelID,
		Exchange:        o.Exchange,
		Symbol:          o.Symbol,
		MarketType:      marketTypeToPB(o.MarketType),
		Side:            mooxpb.OrderSide(orderSidePB(o.Side)),
		PosSide:         o.PosSide,
		OrderType:       mooxpb.OrderType(orderTypePB(o.OrderType)),
		TimeInForce:     o.TimeInForce,
		Price:           o.Price,
		Quantity:        o.Quantity,
		Amount:          o.Amount,
		FilledQty:       o.FilledQty,
		FilledAmount:    o.FilledAmount,
		AvgPrice:        o.AvgPrice,
		Fee:             o.Fee,
		FeeCurrency:     o.FeeCurrency,
		Status:          orderStatusToPB(o.Status),
		ReduceOnly:      o.ReduceOnly,
		TriggerPrice:    o.TriggerPrice,
		Source:          o.Source,
		StrategyId:      o.StrategyID,
		RejectReason:    o.RejectReason,
		SubmittedAt:     unixOrZero(o.SubmittedAt),
		FinishedAt:      unixOrZero(o.FinishedAt),
		CreatedAt:       unixOrZero(o.CreatedAt),
		UpdatedAt:       unixOrZero(o.UpdatedAt),
	}
}

func orderSidePB(s string) int32 {
	if s == "sell" {
		return int32(mooxpb.OrderSide_ORDER_SIDE_SELL)
	}
	return int32(mooxpb.OrderSide_ORDER_SIDE_BUY)
}

func orderTypePB(s string) int32 {
	switch s {
	case "market":
		return int32(mooxpb.OrderType_ORDER_TYPE_MARKET)
	case "stop":
		return int32(mooxpb.OrderType_ORDER_TYPE_STOP)
	case "stop_limit":
		return int32(mooxpb.OrderType_ORDER_TYPE_STOP_LIMIT)
	case "post_only":
		return int32(mooxpb.OrderType_ORDER_TYPE_POST_ONLY)
	case "ioc":
		return int32(mooxpb.OrderType_ORDER_TYPE_IOC)
	case "fok":
		return int32(mooxpb.OrderType_ORDER_TYPE_FOK)
	default:
		return int32(mooxpb.OrderType_ORDER_TYPE_LIMIT)
	}
}

func tradeToPB(t *service.Trade) *mooxpb.Trade {
	if t == nil {
		return nil
	}
	return &mooxpb.Trade{
		TradeId:         t.TradeID,
		ExchangeTradeId: t.ExchangeTradeID,
		OrderId:         t.OrderID,
		ExchangeOrderId: t.ExchangeOrderID,
		AccountId:       t.AccountID,
		ChannelId:       t.ChannelID,
		Exchange:        t.Exchange,
		Symbol:          t.Symbol,
		Side:            mooxpb.OrderSide(orderSidePB(t.Side)),
		Price:           t.Price,
		Quantity:        t.Quantity,
		Amount:          t.Amount,
		Fee:             t.Fee,
		FeeCurrency:     t.FeeCurrency,
		Role:            t.Role,
		TradedAt:        unixOrZero(t.TradedAt),
	}
}

func positionToPB(p *service.Position) *mooxpb.Position {
	if p == nil {
		return nil
	}
	return &mooxpb.Position{
		PositionId:     p.PositionID,
		AccountId:      p.AccountID,
		ChannelId:      p.ChannelID,
		Exchange:       p.Exchange,
		Symbol:         p.Symbol,
		PosSide:        p.PosSide,
		Quantity:       p.Quantity,
		AvgPrice:       p.AvgPrice,
		Leverage:       p.Leverage,
		Margin:         p.Margin,
		LiqPrice:       p.LiqPrice,
		UnrealizedPnl:  p.UnrealizedPnl,
		RealizedPnl:    p.RealizedPnl,
		UpdatedAt:      unixOrZero(p.UpdatedAt),
	}
}

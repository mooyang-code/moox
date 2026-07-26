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

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
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

func retInfo(code tradepb.ErrorCode, msg string) *tradepb.RetInfo {
	return &tradepb.RetInfo{Code: code, Msg: msg}
}

// errToRetInfo 把 service 错误映射为 ErrorCode。
func errToRetInfo(err error) *tradepb.RetInfo {
	if err == nil {
		return retInfo(tradepb.ErrorCode_SUCCESS, "")
	}
	switch err {
	case service.ErrInvalidParam:
		return retInfo(tradepb.ErrorCode_INVALID_PARAM, err.Error())
	case service.ErrNotFound:
		return retInfo(tradepb.ErrorCode_NOT_FOUND, err.Error())
	case service.ErrConflict, service.ErrInsufficient:
		return retInfo(tradepb.ErrorCode_INVALID_PARAM, err.Error())
	default:
		return retInfo(tradepb.ErrorCode_INNER_ERR, err.Error())
	}
}

func pageFromPB(p *tradepb.Page) service.Page {
	if p == nil {
		return service.Page{}
	}
	return service.Page{PageNo: int(p.GetPage()), PageSize: int(p.GetSize())}
}

func pageResult(page service.Page, total int) *tradepb.PageResult {
	return &tradepb.PageResult{
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

func accountTypeToDomain(t tradepb.AccountType) service.AccountType {
	switch t {
	case tradepb.AccountType_ACCOUNT_TYPE_MARGIN:
		return service.AccountMargin
	case tradepb.AccountType_ACCOUNT_TYPE_SWAP:
		return service.AccountSwap
	case tradepb.AccountType_ACCOUNT_TYPE_SIM:
		return service.AccountSim
	default:
		return service.AccountSpot
	}
}

func accountTypeToPB(t service.AccountType) tradepb.AccountType {
	switch t {
	case service.AccountMargin:
		return tradepb.AccountType_ACCOUNT_TYPE_MARGIN
	case service.AccountSwap:
		return tradepb.AccountType_ACCOUNT_TYPE_SWAP
	case service.AccountSim:
		return tradepb.AccountType_ACCOUNT_TYPE_SIM
	default:
		return tradepb.AccountType_ACCOUNT_TYPE_SPOT
	}
}

func accountStatusToPB(s service.AccountStatus) tradepb.AccountStatus {
	switch s {
	case service.AccountDisabled:
		return tradepb.AccountStatus_ACCOUNT_STATUS_DISABLED
	case service.AccountFrozen:
		return tradepb.AccountStatus_ACCOUNT_STATUS_FROZEN
	case service.AccountReadonly:
		return tradepb.AccountStatus_ACCOUNT_STATUS_READONLY
	default:
		return tradepb.AccountStatus_ACCOUNT_STATUS_NORMAL
	}
}

func marketTypeToDomain(m tradepb.MarketType) string {
	switch m {
	case tradepb.MarketType_MARKET_TYPE_MARGIN:
		return "margin"
	case tradepb.MarketType_MARKET_TYPE_SWAP:
		return "swap"
	case tradepb.MarketType_MARKET_TYPE_FUTURES:
		return "futures"
	default:
		return "spot"
	}
}

func marketTypeToPB(s string) tradepb.MarketType {
	switch s {
	case "margin":
		return tradepb.MarketType_MARKET_TYPE_MARGIN
	case "swap":
		return tradepb.MarketType_MARKET_TYPE_SWAP
	case "futures":
		return tradepb.MarketType_MARKET_TYPE_FUTURES
	default:
		return tradepb.MarketType_MARKET_TYPE_SPOT
	}
}

func channelStatusToPB(s int) tradepb.ChannelStatus { return tradepb.ChannelStatus(s) }

// ===== 模型 -> PB =====

func accountToPB(a *service.Account) *tradepb.Account {
	if a == nil {
		return nil
	}
	return &tradepb.Account{
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

func balanceToPB(b *service.Balance) *tradepb.Balance {
	if b == nil {
		return nil
	}
	return &tradepb.Balance{
		AccountId: b.AccountID,
		Currency:  b.Currency,
		Available: b.Available,
		Frozen:    b.Frozen,
		Total:     b.Total,
	}
}

func fundFlowToPB(f *service.FundFlow) *tradepb.FundFlow {
	if f == nil {
		return nil
	}
	return &tradepb.FundFlow{
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

func apiKeyToPB(k *service.APIKey) *tradepb.ApiKey {
	if k == nil {
		return nil
	}
	return &tradepb.ApiKey{
		ApiKeyId:    k.APIKeyID,
		AccountId:   k.AccountID,
		Exchange:    k.Exchange,
		ApiKey:      k.APIKey,
		Permissions: k.PermissionsRaw,
		Status:      int32(k.Status),
		CreatedAt:   unixOrZero(k.CreatedAt),
	}
}

func channelToPB(c *service.TradeChannel) *tradepb.TradeChannel {
	if c == nil {
		return nil
	}
	return &tradepb.TradeChannel{
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

func instrumentToPB(ins exchange.Instrument) *tradepb.Instrument {
	return &tradepb.Instrument{
		Symbol:      ins.Symbol,
		MarketType:  marketTypeToPB(string(ins.Market)),
		BaseCcy:     ins.BaseCcy,
		QuoteCcy:    ins.QuoteCcy,
		TickSize:    ins.TickSize,
		LotSize:     ins.LotSize,
		MinNotional: ins.MinNotional,
		MinQty:      ins.MinQty,
		LastPrice:   ins.LastPrice,
		Status:      ins.Status,
	}
}

func dustTransferItemToPB(item exchange.DustTransferItem) *tradepb.DustTransferItem {
	return &tradepb.DustTransferItem{
		Asset:               item.Asset,
		Amount:              item.Amount,
		OperateTime:         item.OperateTime,
		ServiceChargeAmount: item.ServiceChargeAmount,
		TranId:              item.TranID,
		TransferedAmount:    item.TransferedAmount,
	}
}

func dustTransferSkippedItemToPB(item exchange.DustTransferSkippedItem) *tradepb.DustTransferSkippedItem {
	return &tradepb.DustTransferSkippedItem{
		Asset:  item.Asset,
		Reason: item.Reason,
	}
}

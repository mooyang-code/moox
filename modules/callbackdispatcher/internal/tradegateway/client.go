// Package tradegateway 是 action.TradeClient 的真实实现：经网关调用 Trade 的下架名单与下架清仓接口（交易计划 4.7）。
//
// 网关客户端用 PB 序列化，枚举直接是 proto 类型，不存在 JSON 里枚举写成数字的问题。
// 空间头：名单的四个接口（Block、Unblock、ListBlocked、ListHolders）是全局的，不带空间头；
// FlattenInstrument 和 GetOperatorAction 按空间，带上持有者所在的空间（Trade 的 requiredSpace 从 X-Space-Id 取）。
// 网关本身不检查空间头，只按服务目录里的调用方和方法授权。
package tradegateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// tradeService 是 Trade 下架名单与清仓接口所在的 tRPC 服务。
const tradeService = "trpc.moox.trade.TradeConsoleService"

// maxMessageRunes 是带回处理器的说明的字数上限：它会写进执行记录的步骤里。
const maxMessageRunes = 300

// Client 经共享的网关客户端调用 Trade。
type Client struct{ gateway gatewayclient.Invoker }

var _ action.TradeClient = (*Client)(nil)

// New 创建客户端。
func New(gateway gatewayclient.Invoker) *Client { return &Client{gateway: gateway} }

var exchanges = map[string]tradepb.Exchange{
	"binance": tradepb.Exchange_EXCHANGE_BINANCE,
	"okx":     tradepb.Exchange_EXCHANGE_OKX,
}

var markets = map[string]tradepb.MarketType{
	"SPOT": tradepb.MarketType_MARKET_TYPE_SPOT,
	"SWAP": tradepb.MarketType_MARKET_TYPE_SWAP,
}

func toExchange(name string) (tradepb.Exchange, error) {
	value, ok := exchanges[name]
	if !ok {
		return 0, fmt.Errorf("%w: 不支持的交易所 %q", action.ErrInvalidRequest, name)
	}
	return value, nil
}

func toMarket(name string) (tradepb.MarketType, error) {
	value, ok := markets[name]
	if !ok {
		return 0, fmt.Errorf("%w: 不支持的市场 %q", action.ErrInvalidRequest, name)
	}
	return value, nil
}

func fromExchange(value tradepb.Exchange) string {
	for name, candidate := range exchanges {
		if candidate == value {
			return name
		}
	}
	return ""
}

func fromMarket(value tradepb.MarketType) string {
	for name, candidate := range markets {
		if candidate == value {
			return name
		}
	}
	return ""
}

// invoke 调用 Trade。spaceID 非空时带上空间头（按空间的方法）；为空时不带（全局方法）。
func (c *Client) invoke(ctx context.Context, method, spaceID string, request, response any) error {
	if c == nil || c.gateway == nil {
		return errors.New("Trade 网关客户端未配置")
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	metadata.SpaceID = spaceID
	return c.gateway.Invoke(gatewayclient.WithCallMetadata(ctx, metadata), tradeService, method, request, response)
}

// retError 把 Trade 的返回码翻译成错误：参数不合法单独标出（重试没有用）。
func retError(info *tradepb.RetInfo) error {
	if info == nil || info.GetCode() == tradepb.ErrorCode_SUCCESS {
		return nil
	}
	message := info.GetMsg()
	if message == "" {
		message = info.GetCode().String()
	}
	if info.GetCode() == tradepb.ErrorCode_INVALID_PARAM {
		return fmt.Errorf("%w: %s", action.ErrInvalidRequest, message)
	}
	return fmt.Errorf("Trade 返回 %s：%s", info.GetCode(), message)
}

func (c *Client) instrument(instrument action.InstrumentRef) (tradepb.Exchange, tradepb.MarketType, error) {
	exchange, err := toExchange(instrument.Exchange)
	if err != nil {
		return 0, 0, err
	}
	market, err := toMarket(instrument.Market)
	return exchange, market, err
}

// BlockInstrument 把标的写进下架名单。
func (c *Client) BlockInstrument(ctx context.Context, instrument action.InstrumentRef, reason, eventID string) (action.BlockResult, error) {
	exchange, market, err := c.instrument(instrument)
	if err != nil {
		return action.BlockResult{}, err
	}
	response := new(tradepb.BlockInstrumentRsp)
	if err := c.invoke(ctx, "BlockInstrument", "", &tradepb.BlockInstrumentReq{
		Exchange: exchange, MarketType: market, ExchangeSymbol: instrument.ExchangeSymbol, Reason: reason, EventId: eventID,
	}, response); err != nil {
		return action.BlockResult{}, err
	}
	if err := retError(response.GetRetInfo()); err != nil {
		return action.BlockResult{}, err
	}
	return action.BlockResult{InstrumentUnknown: response.GetInstrumentUnknown(), Created: response.GetCreated()}, nil
}

// UnblockInstrument 解除禁止。
func (c *Client) UnblockInstrument(ctx context.Context, instrument action.InstrumentRef) error {
	exchange, market, err := c.instrument(instrument)
	if err != nil {
		return err
	}
	response := new(tradepb.UnblockInstrumentRsp)
	if err := c.invoke(ctx, "UnblockInstrument", "", &tradepb.UnblockInstrumentReq{
		Exchange: exchange, MarketType: market, ExchangeSymbol: instrument.ExchangeSymbol,
	}, response); err != nil {
		return err
	}
	return retError(response.GetRetInfo())
}

// ListBlockedInstruments 返回当前下架名单。
func (c *Client) ListBlockedInstruments(ctx context.Context) ([]action.InstrumentRef, error) {
	response := new(tradepb.ListBlockedInstrumentsRsp)
	if err := c.invoke(ctx, "ListBlockedInstruments", "", &tradepb.ListBlockedInstrumentsReq{}, response); err != nil {
		return nil, err
	}
	if err := retError(response.GetRetInfo()); err != nil {
		return nil, err
	}
	list := make([]action.InstrumentRef, 0, len(response.GetInstruments()))
	for _, item := range response.GetInstruments() {
		list = append(list, action.InstrumentRef{Exchange: fromExchange(item.GetExchange()), Market: fromMarket(item.GetMarketType()), ExchangeSymbol: item.GetExchangeSymbol()})
	}
	return list, nil
}

// ListInstrumentHolders 跨空间列出持有该标的的执行账户（每个执行账户一行），连同没能判断的账户和说明。响应里没有交易所，持有者的交易所取自请求。
func (c *Client) ListInstrumentHolders(ctx context.Context, instrument action.InstrumentRef, paperOnly bool) (action.HoldersResult, error) {
	exchange, market, err := c.instrument(instrument)
	if err != nil {
		return action.HoldersResult{}, err
	}
	response := new(tradepb.ListInstrumentHoldersRsp)
	if err := c.invoke(ctx, "ListInstrumentHolders", "", &tradepb.ListInstrumentHoldersReq{
		Exchange: exchange, MarketType: market, ExchangeSymbol: instrument.ExchangeSymbol, PaperOnly: paperOnly,
	}, response); err != nil {
		return action.HoldersResult{}, err
	}
	if err := retError(response.GetRetInfo()); err != nil {
		return action.HoldersResult{}, err
	}
	result := action.HoldersResult{InstrumentUnknown: response.GetInstrumentUnknown(), Notes: append([]string(nil), response.GetNotes()...)}
	for _, item := range response.GetHolders() {
		result.Holders = append(result.Holders, action.Holder{
			SpaceID: item.GetSpaceId(), LogicalAccountID: item.GetLogicalAccountId(), Exchange: instrument.Exchange,
			Quantity: item.GetQuantity(), Dust: item.GetDust(), Paper: item.GetPaper(), Status: item.GetStatus(), Note: item.GetNote(),
		})
	}
	for _, item := range response.GetSkippedAccounts() {
		result.Skipped = append(result.Skipped, action.SkippedAccount{TradingAccountID: item.GetTradingAccountId(), Reason: item.GetReason()})
	}
	return result, nil
}

// FlattenInstrument 在持有者所在的空间里对一个组合发起单标的下架清仓。Trade 忙（busy，返回码 CONFLICT、不带动作）时返回 ErrTradeBusy。
func (c *Client) FlattenInstrument(ctx context.Context, request action.FlattenRequest) (action.OperatorAction, error) {
	exchange, market, err := c.instrument(request.Instrument)
	if err != nil {
		return action.OperatorAction{}, err
	}
	response := new(tradepb.FlattenInstrumentRsp)
	if err := c.invoke(ctx, "FlattenInstrument", request.SpaceID, &tradepb.FlattenInstrumentReq{
		ActionId: request.ActionID, LogicalAccountId: request.LogicalAccountID,
		Exchange: exchange, MarketType: market, ExchangeSymbol: request.Instrument.ExchangeSymbol, Reason: request.Reason,
	}, response); err != nil {
		return action.OperatorAction{}, err
	}
	if response.GetBusy() {
		return action.OperatorAction{}, action.ErrTradeBusy
	}
	if err := retError(response.GetRetInfo()); err != nil {
		return action.OperatorAction{}, err
	}
	result := operatorAction(response.GetAction())
	result.Unblocked = result.Unblocked || response.GetUnblocked()
	return result, nil
}

// GetOperatorAction 查询一次操作的结果。
func (c *Client) GetOperatorAction(ctx context.Context, spaceID, operatorActionID string) (action.OperatorAction, error) {
	response := new(tradepb.GetOperatorActionRsp)
	if err := c.invoke(ctx, "GetOperatorAction", spaceID, &tradepb.GetOperatorActionReq{ActionId: operatorActionID}, response); err != nil {
		return action.OperatorAction{}, err
	}
	if err := retError(response.GetRetInfo()); err != nil {
		return action.OperatorAction{}, err
	}
	return operatorAction(response.GetAction()), nil
}

// operatorAction 翻译 Trade 的操作：已解除禁止的标记在结果 JSON 的顶层；说明取失败原因，再补各账户的原因和剩余仓位。
func operatorAction(item *tradepb.OperatorAction) action.OperatorAction {
	result := action.OperatorAction{ActionID: item.GetActionId(), Status: item.GetStatus()}
	var detail struct {
		Unblocked bool `json:"unblocked"`
		Accounts  []struct {
			TradingAccountID string `json:"trading_account_id"`
			Error            string `json:"error"`
			Remaining        []struct {
				Quantity string `json:"quantity"`
				Reason   string `json:"reason"`
			} `json:"remaining_positions"`
		} `json:"accounts"`
	}
	var parts []string
	if message := strings.TrimSpace(item.GetLastError()); message != "" {
		parts = append(parts, message)
	}
	if raw := item.GetResultJson(); raw != "" && json.Unmarshal([]byte(raw), &detail) == nil {
		result.Unblocked = detail.Unblocked
		for _, account := range detail.Accounts {
			if account.Error != "" && !contains(parts, account.Error) {
				parts = append(parts, account.Error)
			}
			for _, remaining := range account.Remaining {
				parts = append(parts, fmt.Sprintf("执行账户 %s 剩余 %s：%s", account.TradingAccountID, remaining.Quantity, remaining.Reason))
			}
		}
	}
	result.Message = truncate(strings.Join(parts, "；"))
	return result
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func truncate(text string) string {
	if runes := []rune(text); len(runes) > maxMessageRunes {
		return string(runes[:maxMessageRunes]) + "…"
	}
	return text
}

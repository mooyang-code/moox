// Package okx 实现 OKX 交易所的 ExchangeAdapter（v5 统一账户，靠 instType/instId 区分市场）。
package okx

import (
	"context"
	"errors"

	ex "github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// errNotImplemented 表示对应接口尚未实现（骨架阶段）。
var errNotImplemented = errors.New("okx: not implemented")

func init() {
	ex.Register("okx", func() ex.ExchangeAdapter { return &Adapter{} })
}

// Adapter 是 OKX 的统一适配实现。
type Adapter struct{}

func (a *Adapter) Name() string { return "okx" }

func (a *Adapter) Ping(ctx context.Context, cred ex.Credential) (int64, error) {
	return 0, errNotImplemented
}

func (a *Adapter) GetInstruments(ctx context.Context, market ex.MarketType) ([]ex.Instrument, error) {
	return nil, errNotImplemented
}

func (a *Adapter) GetAccountInfo(ctx context.Context, cred ex.Credential, market ex.MarketType) (*ex.AccountInfo, error) {
	return nil, errNotImplemented
}

func (a *Adapter) GetBalances(ctx context.Context, cred ex.Credential, market ex.MarketType, currencies []string) ([]ex.Balance, error) {
	return nil, errNotImplemented
}

func (a *Adapter) GetTradeFee(ctx context.Context, cred ex.Credential, market ex.MarketType, symbol string) (*ex.FeeRate, error) {
	return nil, errNotImplemented
}

func (a *Adapter) ListFundFlows(ctx context.Context, cred ex.Credential, req *ex.FundFlowQuery) ([]ex.FundFlow, error) {
	return nil, errNotImplemented
}

func (a *Adapter) Transfer(ctx context.Context, cred ex.Credential, req *ex.TransferReq) (*ex.TransferResult, error) {
	return nil, errNotImplemented
}

func (a *Adapter) PlaceOrder(ctx context.Context, cred ex.Credential, req *ex.PlaceOrderReq) (*ex.OrderResult, error) {
	return nil, errNotImplemented
}

func (a *Adapter) CancelOrder(ctx context.Context, cred ex.Credential, req *ex.CancelOrderReq) (*ex.OrderResult, error) {
	return nil, errNotImplemented
}

// CancelAllOrders 走 POST /api/v5/trade/cancel-all-orders。
func (a *Adapter) CancelAllOrders(ctx context.Context, cred ex.Credential, market ex.MarketType, symbol string) (int, error) {
	return 0, errNotImplemented
}

// AmendOrder 走原生 POST /api/v5/trade/amend-order。
func (a *Adapter) AmendOrder(ctx context.Context, cred ex.Credential, req *ex.AmendOrderReq) (*ex.OrderResult, error) {
	return nil, errNotImplemented
}

func (a *Adapter) SetLeverage(ctx context.Context, cred ex.Credential, market ex.MarketType, symbol, leverage string) error {
	return errNotImplemented
}

func (a *Adapter) ClosePosition(ctx context.Context, cred ex.Credential, market ex.MarketType, symbol, posSide string) error {
	return errNotImplemented
}

func (a *Adapter) GetOrder(ctx context.Context, cred ex.Credential, req *ex.GetOrderReq) (*ex.Order, error) {
	return nil, errNotImplemented
}

func (a *Adapter) ListOpenOrders(ctx context.Context, cred ex.Credential, req *ex.ListOrdersReq) ([]ex.Order, error) {
	return nil, errNotImplemented
}

func (a *Adapter) ListOrders(ctx context.Context, cred ex.Credential, req *ex.ListOrdersReq) ([]ex.Order, error) {
	return nil, errNotImplemented
}

func (a *Adapter) ListTrades(ctx context.Context, cred ex.Credential, req *ex.ListTradesReq) ([]ex.Trade, error) {
	return nil, errNotImplemented
}

func (a *Adapter) ListPositions(ctx context.Context, cred ex.Credential, market ex.MarketType, symbol string) ([]ex.Position, error) {
	return nil, errNotImplemented
}

// 编译期断言：确保 Adapter 实现 ExchangeAdapter。
var _ ex.ExchangeAdapter = (*Adapter)(nil)

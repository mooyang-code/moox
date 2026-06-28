package service

import (
	"context"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// OrderService 实现交易域：交易通道、下单/撤单/改单、订单/成交/持仓查询。
// 下单等操作通过交易通道（channel）解析出账户、凭证与交易所，再委托交易所适配层执行，
// 执行结果落库 t_orders/t_trades，并由账户域刷新余额（同一事务，TODO 在 DAO 实现）。
type OrderService struct {
	store Store
	exNew ExchangeFactory
}

// ---- 交易通道 ----

// CreateChannel 创建交易通道。
func (s *OrderService) CreateChannel(ctx context.Context, spaceID string, c *TradeChannel) (string, error) {
	if c == nil || strings.TrimSpace(c.ChannelName) == "" || c.Exchange == "" {
		return "", ErrInvalidParam
	}
	if c.ChannelID == "" {
		c.ChannelID = genID("ch")
	}
	if c.MarketType == "" {
		c.MarketType = "spot"
	}
	if c.Status == 0 {
		c.Status = 1
	}
	if err := s.store.CreateChannel(ctx, spaceID, c); err != nil {
		return "", err
	}
	return c.ChannelID, nil
}

// UpdateChannel 更新交易通道。
func (s *OrderService) UpdateChannel(ctx context.Context, spaceID string, c *TradeChannel) error {
	if c == nil || c.ChannelID == "" {
		return ErrInvalidParam
	}
	return s.store.UpdateChannel(ctx, spaceID, c)
}

// DeleteChannel 软删除交易通道。
func (s *OrderService) DeleteChannel(ctx context.Context, spaceID, channelID string) error {
	if channelID == "" {
		return ErrInvalidParam
	}
	return s.store.DeleteChannel(ctx, spaceID, channelID)
}

// ListChannels 分页查询交易通道。
func (s *OrderService) ListChannels(ctx context.Context, spaceID string, f ChannelFilter, page Page) ([]*TradeChannel, int, error) {
	return s.store.ListChannels(ctx, spaceID, f, page.Normalize())
}

// TestChannel 连通性测试：用通道绑定的凭证 Ping 交易所。
func (s *OrderService) TestChannel(ctx context.Context, spaceID, channelID string) (reachable bool, latencyMS int64, err error) {
	ch, err := s.store.GetChannel(ctx, spaceID, channelID)
	if err != nil {
		return false, 0, err
	}
	adapter, cred, err := s.adapterForChannel(ctx, spaceID, ch)
	if err != nil {
		return false, 0, err
	}
	latencyMS, err = adapter.Ping(ctx, cred)
	if err != nil {
		return false, latencyMS, err
	}
	return true, latencyMS, nil
}

// ---- 账户交易操作 ----

// PlaceOrder 下单：委托 PlaceOrderExec 完成冻结→落库→适配层→审计编排。
func (s *OrderService) PlaceOrder(ctx context.Context, spaceID string, channelID string, req *exchange.PlaceOrderReq) (*Order, error) {
	return s.PlaceOrderExec(ctx, spaceID, channelID, req, operatorFromContext(ctx))
}

// CancelOrder 撤单：委托 CancelOrderExec 完成适配层→解冻→审计编排。
func (s *OrderService) CancelOrder(ctx context.Context, spaceID, channelID string, req *exchange.CancelOrderReq) (*Order, error) {
	return s.CancelOrderExec(ctx, spaceID, channelID, req, operatorFromContext(ctx))
}

// CancelAllOrders 全撤（可按 symbol 过滤）。
func (s *OrderService) CancelAllOrders(ctx context.Context, spaceID, channelID, symbol string) (int, error) {
	if channelID == "" {
		return 0, ErrInvalidParam
	}
	ch, err := s.store.GetChannel(ctx, spaceID, channelID)
	if err != nil {
		return 0, err
	}
	adapter, cred, err := s.adapterForChannel(ctx, spaceID, ch)
	if err != nil {
		return 0, err
	}
	return adapter.CancelAllOrders(ctx, cred, exchange.MarketType(ch.MarketType), symbol)
}

// AmendOrder 改单：委托 AmendOrderExec 完成适配层→更新→审计编排。
func (s *OrderService) AmendOrder(ctx context.Context, spaceID, channelID string, req *exchange.AmendOrderReq) (*Order, error) {
	return s.AmendOrderExec(ctx, spaceID, channelID, req, operatorFromContext(ctx))
}

// SetLeverage 调整杠杆。
func (s *OrderService) SetLeverage(ctx context.Context, spaceID, channelID, symbol, leverage string) error {
	if channelID == "" || symbol == "" || leverage == "" {
		return ErrInvalidParam
	}
	ch, err := s.store.GetChannel(ctx, spaceID, channelID)
	if err != nil {
		return err
	}
	adapter, cred, err := s.adapterForChannel(ctx, spaceID, ch)
	if err != nil {
		return err
	}
	return adapter.SetLeverage(ctx, cred, exchange.MarketType(ch.MarketType), symbol, leverage)
}

// ---- 查询 ----

// GetOrder 查询单个订单（本地库）。
func (s *OrderService) GetOrder(ctx context.Context, spaceID, orderID, clientOrderID string) (*Order, error) {
	if orderID == "" && clientOrderID == "" {
		return nil, ErrInvalidParam
	}
	return s.store.GetOrder(ctx, spaceID, orderID, clientOrderID)
}

// ListOrders 分页查询订单（本地库）。
func (s *OrderService) ListOrders(ctx context.Context, spaceID string, f OrderFilter, page Page) ([]*Order, int, error) {
	if f.AccountID == "" {
		return nil, 0, ErrInvalidParam
	}
	return s.store.ListOrders(ctx, spaceID, f, page.Normalize())
}

// ListTrades 分页查询成交明细（本地库）。
func (s *OrderService) ListTrades(ctx context.Context, spaceID string, f TradeFilter, page Page) ([]*Trade, int, error) {
	if f.AccountID == "" {
		return nil, 0, ErrInvalidParam
	}
	return s.store.ListTrades(ctx, spaceID, f, page.Normalize())
}

// ListPositions 查询持仓（本地库）。
func (s *OrderService) ListPositions(ctx context.Context, spaceID, accountID, symbol string) ([]*Position, error) {
	if accountID == "" {
		return nil, ErrInvalidParam
	}
	return s.store.ListPositions(ctx, spaceID, accountID, symbol)
}

// adapterForChannel 由交易通道解析出交易所适配器与解密后的凭证。
func (s *OrderService) adapterForChannel(ctx context.Context, spaceID string, ch *TradeChannel) (exchange.ExchangeAdapter, exchange.Credential, error) {
	var cred exchange.Credential
	if ch == nil {
		return nil, cred, ErrInvalidParam
	}
	adapter, err := s.exNew(ch.Exchange)
	if err != nil {
		return nil, cred, err
	}
	if ch.APIKeyID != "" {
		k, err := s.store.GetAPIKey(ctx, spaceID, ch.APIKeyID)
		if err != nil {
			return nil, cred, err
		}
		cred = exchange.Credential{APIKey: k.APIKey, APISecret: k.APISecret, Passphrase: k.Passphrase}
	}
	return adapter, cred, nil
}

// ResolveAdapter 由 channel_id 解析出适配器、解密凭证与通道对象（供 RPC 层 SyncBalances 等使用）。
func (s *OrderService) ResolveAdapter(ctx context.Context, spaceID, channelID string) (exchange.ExchangeAdapter, exchange.Credential, *TradeChannel, error) {
	ch, err := s.store.GetChannel(ctx, spaceID, channelID)
	if err != nil {
		return nil, exchange.Credential{}, nil, err
	}
	adapter, cred, err := s.adapterForChannel(ctx, spaceID, ch)
	return adapter, cred, ch, err
}

// NewAdapter 按交易所名创建适配器（供 RPC 层直接使用）。
func (s *OrderService) NewAdapter(name string) (exchange.ExchangeAdapter, error) {
	return s.exNew(name)
}

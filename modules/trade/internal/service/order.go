package service

import (
	"context"
	"strings"
	"time"

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

// ListInstruments 拉取通道对应市场的交易规则。
func (s *OrderService) ListInstruments(ctx context.Context, spaceID, channelID string, market exchange.MarketType) ([]exchange.Instrument, error) {
	if channelID == "" {
		return nil, ErrInvalidParam
	}
	ch, err := s.store.GetChannel(ctx, spaceID, channelID)
	if err != nil {
		return nil, err
	}
	adapter, _, err := s.adapterForChannel(ctx, spaceID, ch)
	if err != nil {
		return nil, err
	}
	if market == "" {
		market = exchangeMarketType(ch.MarketType)
	}
	return adapter.GetInstruments(ctx, market)
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

// ConvertDust 将现货小额资产转换为交易所支持的目标资产。Binance 现货目标为 BNB。
func (s *OrderService) ConvertDust(ctx context.Context, spaceID, channelID string, assets []string) (*exchange.DustTransferResult, error) {
	if channelID == "" || len(assets) == 0 {
		return nil, ErrInvalidParam
	}
	normalizedAssets := normalizeDustAssets(assets)
	if len(normalizedAssets) == 0 {
		return nil, ErrInvalidParam
	}
	ch, err := s.store.GetChannel(ctx, spaceID, channelID)
	if err != nil {
		return nil, err
	}
	adapter, cred, err := s.adapterForChannel(ctx, spaceID, ch)
	if err != nil {
		return nil, err
	}
	convertible, err := adapter.ListConvertibleDustAssets(ctx, cred, &exchange.DustConvertibleReq{AccountType: "SPOT"})
	if err != nil {
		return nil, err
	}
	convertibleSet := make(map[string]bool, len(convertible))
	for _, item := range convertible {
		asset := strings.ToUpper(strings.TrimSpace(item.Asset))
		if asset != "" {
			convertibleSet[asset] = true
		}
	}
	eligibleAssets := make([]string, 0, len(normalizedAssets))
	skipped := make([]exchange.DustTransferSkippedItem, 0)
	for _, asset := range normalizedAssets {
		if convertibleSet[asset] {
			eligibleAssets = append(eligibleAssets, asset)
			continue
		}
		skipped = append(skipped, exchange.DustTransferSkippedItem{
			Asset:  asset,
			Reason: "估值过低，Binance 不支持转换，保留小尾巴",
		})
	}
	if len(eligibleAssets) == 0 {
		return &exchange.DustTransferResult{Skipped: skipped}, nil
	}
	out, err := adapter.ConvertDust(ctx, cred, &exchange.DustTransferReq{
		Assets:      eligibleAssets,
		AccountType: "SPOT",
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = &exchange.DustTransferResult{}
	}
	out.Skipped = append(out.Skipped, skipped...)
	return out, nil
}

func normalizeDustAssets(assets []string) []string {
	out := make([]string, 0, len(assets))
	seen := make(map[string]bool, len(assets))
	for _, asset := range assets {
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if asset == "" || seen[asset] {
			continue
		}
		seen[asset] = true
		out = append(out, asset)
	}
	return out
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

// SyncOrders 从交易所拉取订单快照并刷新本地订单表。
func (s *OrderService) SyncOrders(ctx context.Context, spaceID, accountID, symbol string, onlyOpen bool, startTime, endTime int64, page Page) ([]*Order, int, error) {
	if accountID == "" {
		return nil, 0, ErrInvalidParam
	}
	account, err := s.store.GetAccount(ctx, spaceID, accountID)
	if err != nil {
		return nil, 0, err
	}
	if account.ChannelID == "" {
		return nil, 0, ErrInvalidParam
	}
	adapter, cred, ch, err := s.ResolveAdapter(ctx, spaceID, account.ChannelID)
	if err != nil {
		return nil, 0, err
	}
	normalizedPage := page.Normalize()
	req := &exchange.ListOrdersReq{
		Market:    exchange.MarketType(ch.MarketType),
		Symbol:    strings.ToUpper(strings.TrimSpace(symbol)),
		OnlyOpen:  onlyOpen,
		StartTime: exchangeMillis(startTime),
		EndTime:   exchangeMillis(endTime),
		Limit:     normalizedPage.PageSize,
	}
	var exOrders []exchange.Order
	if onlyOpen {
		exOrders, err = adapter.ListOpenOrders(ctx, cred, req)
	} else {
		exOrders, err = adapter.ListOrders(ctx, cred, req)
	}
	if err != nil {
		return nil, 0, err
	}
	orders := make([]*Order, 0, len(exOrders))
	for _, o := range exOrders {
		orders = append(orders, serviceOrderFromExchange(accountID, ch, o))
	}
	if err := s.store.UpsertOrders(ctx, spaceID, orders); err != nil {
		return nil, 0, err
	}
	return s.store.ListOrders(ctx, spaceID, OrderFilter{
		AccountID: accountID,
		Symbol:    req.Symbol,
		OnlyOpen:  onlyOpen,
		StartTime: startTime,
		EndTime:   endTime,
	}, normalizedPage)
}

// SyncTrades 从交易所拉取成交明细并追加到本地成交表。
func (s *OrderService) SyncTrades(ctx context.Context, spaceID, accountID, symbol, orderID string, startTime, endTime int64, page Page) ([]*Trade, int, error) {
	if accountID == "" {
		return nil, 0, ErrInvalidParam
	}
	account, err := s.store.GetAccount(ctx, spaceID, accountID)
	if err != nil {
		return nil, 0, err
	}
	if account.ChannelID == "" {
		return nil, 0, ErrInvalidParam
	}
	adapter, cred, ch, err := s.ResolveAdapter(ctx, spaceID, account.ChannelID)
	if err != nil {
		return nil, 0, err
	}
	normalizedPage := page.Normalize()
	req := &exchange.ListTradesReq{
		Market:    exchange.MarketType(ch.MarketType),
		Symbol:    strings.ToUpper(strings.TrimSpace(symbol)),
		OrderID:   strings.TrimSpace(orderID),
		StartTime: exchangeMillis(startTime),
		EndTime:   exchangeMillis(endTime),
		Limit:     normalizedPage.PageSize,
	}
	exTrades, err := adapter.ListTrades(ctx, cred, req)
	if err != nil {
		return nil, 0, err
	}
	trades := make([]*Trade, 0, len(exTrades))
	for _, tr := range exTrades {
		trades = append(trades, serviceTradeFromExchange(accountID, ch, tr))
	}
	if err := s.store.AppendTrades(ctx, spaceID, trades); err != nil {
		return nil, 0, err
	}
	return s.store.ListTrades(ctx, spaceID, TradeFilter{
		AccountID: accountID,
		OrderID:   orderID,
		Symbol:    req.Symbol,
		StartTime: startTime,
		EndTime:   endTime,
	}, normalizedPage)
}

// ListPositions 查询持仓（本地库）。
func (s *OrderService) ListPositions(ctx context.Context, spaceID, accountID, symbol string) ([]*Position, error) {
	if accountID == "" {
		return nil, ErrInvalidParam
	}
	return s.store.ListPositions(ctx, spaceID, accountID, symbol)
}

// SyncPositions 从交易所拉取当前持仓并替换本地快照。
func (s *OrderService) SyncPositions(ctx context.Context, spaceID, accountID, symbol string) ([]*Position, error) {
	if accountID == "" {
		return nil, ErrInvalidParam
	}
	account, err := s.store.GetAccount(ctx, spaceID, accountID)
	if err != nil {
		return nil, err
	}
	if account.ChannelID == "" {
		return nil, ErrInvalidParam
	}
	adapter, cred, ch, err := s.ResolveAdapter(ctx, spaceID, account.ChannelID)
	if err != nil {
		return nil, err
	}
	exPositions, err := adapter.ListPositions(ctx, cred, exchange.MarketType(ch.MarketType), strings.ToUpper(strings.TrimSpace(symbol)))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	domain := make([]*Position, 0, len(exPositions))
	for _, p := range exPositions {
		posSide := p.PosSide
		if posSide == "" {
			posSide = "net"
		}
		updatedAt := now
		if p.UpdatedAt > 0 {
			updatedAt = time.UnixMilli(p.UpdatedAt)
		}
		domain = append(domain, &Position{
			PositionID:    deterministicID("pos", accountID+"|"+p.Symbol+"|"+posSide),
			AccountID:     accountID,
			ChannelID:     ch.ChannelID,
			Exchange:      ch.Exchange,
			Symbol:        p.Symbol,
			PosSide:       posSide,
			Quantity:      p.Quantity,
			AvgPrice:      p.AvgPrice,
			Leverage:      defaultString(p.Leverage, "1"),
			Margin:        defaultString(p.Margin, "0"),
			LiqPrice:      defaultString(p.LiqPrice, "0"),
			UnrealizedPnl: defaultString(p.UnrealizedPnl, "0"),
			RealizedPnl:   defaultString(p.RealizedPnl, "0"),
			UpdatedAt:     updatedAt,
		})
	}
	filterSymbol := strings.ToUpper(strings.TrimSpace(symbol))
	if err := s.store.ReplacePositions(ctx, spaceID, accountID, filterSymbol, domain); err != nil {
		return nil, err
	}
	return s.store.ListPositions(ctx, spaceID, accountID, filterSymbol)
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

func serviceOrderFromExchange(accountID string, ch *TradeChannel, o exchange.Order) *Order {
	exchangeOrderID := strings.TrimSpace(o.ExchangeOrderID)
	if exchangeOrderID == "" {
		exchangeOrderID = strings.TrimSpace(o.OrderID)
	}
	clientOrderID := strings.TrimSpace(o.ClientOrderID)
	if clientOrderID == "" {
		clientOrderID = exchangeOrderID
	}
	orderIDSource := exchangeOrderID
	if orderIDSource == "" {
		orderIDSource = clientOrderID
	}
	submittedAt := timeFromExchangeMillis(o.CreatedAt)
	updatedAt := timeFromExchangeMillis(o.UpdatedAt)
	finishedAt := time.Time{}
	if isTerminalOrderStatus(o.Status) {
		finishedAt = updatedAt
	}
	if o.Market == "" {
		o.Market = exchange.MarketType(ch.MarketType)
	}
	return &Order{
		OrderID:         deterministicID("o", accountID+"|"+orderIDSource),
		ClientOrderID:   clientOrderID,
		ExchangeOrderID: exchangeOrderID,
		AccountID:       accountID,
		ChannelID:       ch.ChannelID,
		Exchange:        ch.Exchange,
		Symbol:          strings.ToUpper(strings.TrimSpace(o.Symbol)),
		MarketType:      string(o.Market),
		Side:            string(o.Side),
		PosSide:         o.PosSide,
		OrderType:       string(o.Type),
		TimeInForce:     "GTC",
		Price:           defaultString(o.Price, "0"),
		Quantity:        defaultString(o.Quantity, "0"),
		FilledQty:       defaultString(o.FilledQty, "0"),
		FilledAmount:    defaultString(o.FilledAmount, "0"),
		AvgPrice:        defaultString(o.AvgPrice, "0"),
		Fee:             defaultString(o.Fee, "0"),
		FeeCurrency:     o.FeeCurrency,
		Status:          int(o.Status),
		Source:          "exchange_sync",
		SubmittedAt:     submittedAt,
		FinishedAt:      finishedAt,
		UpdatedAt:       updatedAt,
	}
}

func serviceTradeFromExchange(accountID string, ch *TradeChannel, tr exchange.Trade) *Trade {
	exchangeTradeID := strings.TrimSpace(tr.ExchangeTradeID)
	if exchangeTradeID == "" {
		exchangeTradeID = strings.TrimSpace(tr.TradeID)
	}
	exchangeOrderID := strings.TrimSpace(tr.OrderID)
	return &Trade{
		TradeID:         deterministicID("tr", accountID+"|"+exchangeTradeID),
		ExchangeTradeID: exchangeTradeID,
		OrderID:         deterministicID("o", accountID+"|"+exchangeOrderID),
		ExchangeOrderID: exchangeOrderID,
		AccountID:       accountID,
		ChannelID:       ch.ChannelID,
		Exchange:        ch.Exchange,
		Symbol:          strings.ToUpper(strings.TrimSpace(tr.Symbol)),
		Side:            string(tr.Side),
		Price:           defaultString(tr.Price, "0"),
		Quantity:        defaultString(tr.Quantity, "0"),
		Amount:          defaultString(tr.Amount, "0"),
		Fee:             defaultString(tr.Fee, "0"),
		FeeCurrency:     tr.FeeCurrency,
		Role:            tr.Role,
		TradedAt:        timeFromExchangeMillis(tr.TradedAt),
	}
}

func exchangeMillis(v int64) int64 {
	if v > 0 && v < 1_000_000_000_000 {
		return v * 1000
	}
	return v
}

func timeFromExchangeMillis(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(exchangeMillis(v))
}

func isTerminalOrderStatus(status exchange.OrderStatus) bool {
	switch status {
	case exchange.StatusFilled, exchange.StatusCanceled, exchange.StatusPartialCanceled, exchange.StatusRejected, exchange.StatusExpired:
		return true
	default:
		return false
	}
}

package service

import (
	"context"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// OrderService 管理交易通道，以及不产生交易事实的交易所账户操作。
// 订单、成交和持仓统一由 application/kernel 读写，避免形成第二套事实来源。
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

package wiring

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

// AccountSyncer 把账户同步服务适配成下单、人工操作与组合服务所需的同步端口。
type AccountSyncer struct {
	Service *accountsync.Service
}

func (s AccountSyncer) SyncAccount(ctx context.Context, accountID string) error {
	_, err := s.Service.SyncAccount(ctx, accountID)
	return err
}

func (s AccountSyncer) SyncAccountLight(ctx context.Context, accountID string) error {
	_, err := s.Service.SyncAccountLight(ctx, accountID)
	return err
}

func (s AccountSyncer) ConfirmCancel(ctx context.Context, spaceID, orderID string) error {
	return s.Service.ConfirmCancel(ctx, spaceID, orderID)
}

// InstrumentSource 从交易库读取标的规则并转换成下单校验器使用的标的模型。
type InstrumentSource struct {
	Store *store.Store
}

func (s InstrumentSource) GetInstrument(
	ctx context.Context,
	exchangeName exchange.Exchange,
	market exchange.MarketType,
	symbol string,
) (exchange.Instrument, error) {
	// 订单规格里带的是内部标的 ID，只在适配器边界换回交易所原生代码。
	record, err := s.Store.GetInstrumentByIDScoped(ctx, symbol, string(exchangeName), string(market))
	if err != nil {
		return exchange.Instrument{}, err
	}
	if record.MarketType != string(market) {
		return exchange.Instrument{}, fmt.Errorf("标的 %s 属于市场 %s，不是 %s", symbol, record.MarketType, market)
	}
	return InstrumentFromRecord(exchangeName, exchange.MarketType(record.MarketType), record), nil
}

func (s InstrumentSource) GetInstrumentForAccount(
	ctx context.Context,
	tradingAccountID string,
	exchangeName exchange.Exchange,
	market exchange.MarketType,
	symbol string,
) (exchange.Instrument, error) {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return exchange.Instrument{}, err
	}
	record, err := s.Store.GetInstrumentByIDForAccount(ctx, account.SpaceID, tradingAccountID, symbol)
	if err != nil {
		return exchange.Instrument{}, err
	}
	if record.MarketType != string(market) {
		return exchange.Instrument{}, fmt.Errorf("标的 %s 属于市场 %s，不是 %s", symbol, record.MarketType, market)
	}
	return InstrumentFromRecord(exchangeName, exchange.MarketType(record.MarketType), record), nil
}

// InstrumentFromRecord 把交易库的标的记录转换成领域标的模型。
func InstrumentFromRecord(exchangeName exchange.Exchange, market exchange.MarketType, record store.InstrumentRecord) exchange.Instrument {
	return exchange.Instrument{
		Exchange: exchangeName, MarketType: market, ExchangeSymbol: record.ExchangeSymbol,
		InstrumentID: record.InstrumentID, BaseAsset: record.BaseAsset,
		QuoteAsset: record.QuoteAsset, SettlementAsset: record.SettlementAsset,
		Linear: record.Linear, ContractValue: parseDecimalOrZero(record.ContractValue),
		ContractValueAsset:   record.ContractValueAsset,
		ExchangeQuantityStep: parseDecimalOrZero(record.ExchangeQuantityStep),
		MinExchangeQuantity:  parseDecimalOrZero(record.MinExchangeQuantity),
		PriceTick:            parseDecimalOrZero(record.PriceTick), MinNotional: parseDecimalOrZero(record.MinNotional),
		MaxBuyPriceRatio: parseDecimalOrZero(record.MaxBuyPriceRatio), MinSellPriceRatio: parseDecimalOrZero(record.MinSellPriceRatio),
		Status: record.Status, ExchangeUpdatedAt: time.UnixMilli(record.ExchangeUpdatedAt),
	}
}

// PositionSource 从交易库读取持仓并转换成下单校验器使用的持仓模型。
type PositionSource struct {
	Store *store.Store
}

// GetPosition 按内部标的 ID 取持仓；标的 ID 自带市场，现货和合约同名的交易所代码不会串。
func (s PositionSource) GetPosition(
	ctx context.Context,
	tradingAccountID string,
	instrumentID string,
) (exchange.Position, error) {
	return s.GetPositionForAccount(ctx, tradingAccountID, instrumentID)
}

func (s PositionSource) GetPositionForAccount(
	ctx context.Context,
	tradingAccountID string,
	symbol string,
) (exchange.Position, error) {
	account, err := s.Store.GetTradingAccountByID(ctx, tradingAccountID)
	if err != nil {
		return exchange.Position{}, err
	}
	instrument, err := s.Store.GetInstrumentByIDForAccount(ctx, account.SpaceID, tradingAccountID, symbol)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return exchange.Position{TradingAccountID: tradingAccountID, InstrumentID: symbol, PositionSide: exchange.PositionSideNet}, nil
		}
		return exchange.Position{}, err
	}
	key := exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol)
	record, found, err := s.Store.GetPosition(ctx, account.SpaceID, tradingAccountID, key, string(exchange.PositionSideNet))
	if err != nil {
		return exchange.Position{}, err
	}
	if !found {
		return exchange.Position{TradingAccountID: tradingAccountID, Market: key.Market, InstrumentID: symbol, ExchangeSymbol: key.Symbol, PositionSide: exchange.PositionSideNet}, nil
	}
	return exchange.Position{
		TradingAccountID: tradingAccountID, Market: exchange.MarketType(record.MarketType), InstrumentID: record.InstrumentID,
		ExchangeSymbol: record.ExchangeSymbol,
		PositionSide:   exchange.PositionSide(record.PositionSide), SignedQuantity: parseDecimalOrZero(record.SignedQuantity),
		EntryPrice: parseDecimalOrZero(record.EntryPrice), MarkPrice: parseDecimalOrZero(record.MarkPrice), Leverage: parseDecimalOrZero(record.Leverage),
		MarginMode: exchange.MarginMode(record.MarginMode), UsedMargin: parseDecimalOrZero(record.UsedMargin),
		LiquidationPrice: parseDecimalOrZero(record.LiquidationPrice), UnrealizedPnL: parseDecimalOrZero(record.UnrealizedPnL),
		RealizedPnL: parseDecimalOrZero(record.RealizedPnL), ExchangeUpdatedAt: time.UnixMilli(record.ExchangeUpdatedAt),
	}, nil
}

// MarketTypes 把字符串市场列表转成领域市场类型列表。
func MarketTypes(markets []string) []exchange.MarketType {
	result := make([]exchange.MarketType, len(markets))
	for i, market := range markets {
		result[i] = exchange.MarketType(market)
	}
	return result
}

func parseDecimalOrZero(value string) shared.Decimal {
	if strings.TrimSpace(value) == "" {
		return shared.Zero()
	}
	parsed, err := shared.ParseDecimal(value)
	if err != nil {
		return shared.Zero()
	}
	return parsed
}

package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// BlockedInstrumentRecord 是下架名单里的一条：全局的，不分空间，键是（交易所、市场、交易所代码）。
// 内部标的 ID 和基础资产只用于展示和审计，写入时查不到标的表则为空；匹配一律在使用时查标的表。
type BlockedInstrumentRecord struct {
	Exchange       string
	MarketType     string
	ExchangeSymbol string
	InstrumentID   string
	BaseAsset      string
	Reason         string
	SourceEventID  string
	BlockedAt      int64
}

type blockedInstrumentRow struct {
	Exchange       string `gorm:"column:c_exchange"`
	MarketType     string `gorm:"column:c_market_type"`
	ExchangeSymbol string `gorm:"column:c_exchange_symbol"`
	InstrumentID   string `gorm:"column:c_instrument_id"`
	BaseAsset      string `gorm:"column:c_base_asset"`
	Reason         string `gorm:"column:c_reason"`
	SourceEventID  string `gorm:"column:c_source_event_id"`
	BlockedAt      int64  `gorm:"column:c_blocked_at"`
}

const (
	// 重复写入保留首次的写入时间和原因，不改已有的行。
	sqlInsertBlockedInstrument = `
		INSERT INTO t_trade_blocked_instruments (
			c_exchange, c_market_type, c_exchange_symbol,
			c_instrument_id, c_base_asset, c_reason, c_source_event_id, c_blocked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (c_exchange, c_market_type, c_exchange_symbol) DO NOTHING`
	sqlDeleteBlockedInstrument = `
		DELETE FROM t_trade_blocked_instruments
		WHERE c_exchange = ? AND c_market_type = ? AND c_exchange_symbol = ?`
	sqlListBlockedInstruments = `
		SELECT c_exchange, c_market_type, c_exchange_symbol, c_instrument_id, c_base_asset,
			c_reason, c_source_event_id, c_blocked_at
		FROM t_trade_blocked_instruments
		ORDER BY c_blocked_at, c_exchange, c_market_type, c_exchange_symbol`
)

// BlockInstrument 把标的写进下架名单；已经在名单里时不改动已有记录，返回 created=false。
func (s *Store) BlockInstrument(ctx context.Context, record BlockedInstrumentRecord) (bool, error) {
	record.Exchange = strings.ToUpper(strings.TrimSpace(record.Exchange))
	record.MarketType = strings.ToUpper(strings.TrimSpace(record.MarketType))
	record.ExchangeSymbol = strings.ToUpper(strings.TrimSpace(record.ExchangeSymbol))
	record.Reason = strings.TrimSpace(record.Reason)
	if record.Exchange == "" || record.MarketType == "" || record.ExchangeSymbol == "" || record.Reason == "" {
		return false, fmt.Errorf("%w: incomplete blocked instrument", ErrInvalidRecord)
	}
	if record.BlockedAt <= 0 {
		record.BlockedAt = time.Now().UnixMilli()
	}
	result := s.db.WithContext(ctx).Exec(sqlInsertBlockedInstrument,
		record.Exchange, record.MarketType, record.ExchangeSymbol,
		record.InstrumentID, record.BaseAsset, record.Reason, record.SourceEventID, record.BlockedAt)
	if err := writeError(result.Error); err != nil {
		return false, err
	}
	return result.RowsAffected > 0, nil
}

// UnblockInstrument 把标的从下架名单里去掉；不在名单里返回 removed=false，不算错误。
func (s *Store) UnblockInstrument(ctx context.Context, exchange, marketType, exchangeSymbol string) (bool, error) {
	result := s.db.WithContext(ctx).Exec(sqlDeleteBlockedInstrument,
		strings.ToUpper(strings.TrimSpace(exchange)), strings.ToUpper(strings.TrimSpace(marketType)), strings.ToUpper(strings.TrimSpace(exchangeSymbol)))
	if err := writeError(result.Error); err != nil {
		return false, err
	}
	return result.RowsAffected > 0, nil
}

// ListBlockedInstruments 按写入时间返回整张下架名单。
func (s *Store) ListBlockedInstruments(ctx context.Context) ([]BlockedInstrumentRecord, error) {
	var rows []blockedInstrumentRow
	if err := s.db.WithContext(ctx).Raw(sqlListBlockedInstruments).Scan(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]BlockedInstrumentRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, BlockedInstrumentRecord(row))
	}
	return records, nil
}

type blockKey struct{ exchange, marketType, symbol string }

// BlockedSet 是下架名单的匹配视图：执行器、目标接收、清仓、持有者查询都用同一套匹配规则。
// 匹配只看（交易所、市场、交易所代码），不用名单里存的内部标的 ID 和基础资产，所以以后才开通这个市场的账户也能匹配上。
type BlockedSet struct {
	keys map[blockKey]struct{}
}

// LoadBlockedSet 读出整张名单做成匹配视图。
func (s *Store) LoadBlockedSet(ctx context.Context) (BlockedSet, error) {
	records, err := s.ListBlockedInstruments(ctx)
	if err != nil {
		return BlockedSet{}, err
	}
	return NewBlockedSet(records), nil
}

func NewBlockedSet(records []BlockedInstrumentRecord) BlockedSet {
	set := BlockedSet{keys: make(map[blockKey]struct{}, len(records))}
	for _, record := range records {
		set.keys[blockKey{record.Exchange, record.MarketType, record.ExchangeSymbol}] = struct{}{}
	}
	return set
}

func (b BlockedSet) Empty() bool { return len(b.keys) == 0 }

// Contains 报告（交易所、市场、交易所代码）是否在名单里。
func (b BlockedSet) Contains(exchange, marketType, exchangeSymbol string) bool {
	_, found := b.keys[blockKey{exchange, marketType, exchangeSymbol}]
	return found
}

// ContainsInstrument 报告一个标的在指定交易所的账户上是否在名单里。
func (b BlockedSet) ContainsInstrument(exchange string, instrument InstrumentRecord) bool {
	return b.Contains(exchange, instrument.MarketType, instrument.ExchangeSymbol)
}

// SpotBaseAssets 返回指定交易所上名单里 SPOT 条目对应的基础资产：条目在账户标的表（含已下架的）里查 base_asset。
// 只看 SPOT 条目，永续下架不会把同名的现货余额也豁免掉。
func (b BlockedSet) SpotBaseAssets(exchange string, instruments []InstrumentRecord) map[string]struct{} {
	assets := make(map[string]struct{})
	if b.Empty() {
		return assets
	}
	for _, instrument := range instruments {
		if instrument.MarketType == "SPOT" && b.Contains(exchange, "SPOT", instrument.ExchangeSymbol) {
			assets[instrument.BaseAsset] = struct{}{}
		}
	}
	return assets
}

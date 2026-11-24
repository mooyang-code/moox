package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"gorm.io/gorm"
)

// 名单与下架记录的范围：ALL 对所有账户生效；PAPER_ONLY 只对模拟盘账户生效（演练）。
const (
	DelistScopeAll       = "ALL"
	DelistScopePaperOnly = "PAPER_ONLY"
)

// 下架记录的状态。ACTIVE 之外都是终态。
const (
	DelistActive      = "ACTIVE"
	DelistCompleted   = "COMPLETED"
	DelistFailed      = "FAILED"
	DelistCanceled    = "CANCELED"
	DelistRescheduled = "RESCHEDULED"
)

// BlockedInstrumentRecord 是下架名单里的一条：全局的，不分空间，键是（交易所、市场、交易所代码）。
// 内部标的 ID 和基础资产只用于展示和审计，写入时查不到标的表则为空；匹配一律在使用时查标的表。
type BlockedInstrumentRecord struct {
	Exchange       string `gorm:"column:c_exchange"`
	MarketType     string `gorm:"column:c_market_type"`
	ExchangeSymbol string `gorm:"column:c_exchange_symbol"`
	// Scope 是 ALL 或 PAPER_ONLY。
	Scope        string `gorm:"column:c_scope"`
	InstrumentID string `gorm:"column:c_instrument_id"`
	BaseAsset    string `gorm:"column:c_base_asset"`
	Reason       string `gorm:"column:c_reason"`
	// DelistID 是写入或把范围升级为 ALL 的那条下架记录。
	DelistID  string `gorm:"column:c_delist_id"`
	BlockedAt int64  `gorm:"column:c_blocked_at"`
}

// DelistRecord 是一条下架记录：一个（标的、范围、下架时间）。时间一律是 Unix 毫秒。
type DelistRecord struct {
	DelistID        string `gorm:"column:c_delist_id"`
	Exchange        string `gorm:"column:c_exchange"`
	MarketType      string `gorm:"column:c_market_type"`
	ExchangeSymbol  string `gorm:"column:c_exchange_symbol"`
	Scope           string `gorm:"column:c_scope"`
	DelistAt        int64  `gorm:"column:c_delist_at"`
	Source          string `gorm:"column:c_source"`
	SourceEventID   string `gorm:"column:c_source_event_id"`
	EventOccurredAt int64  `gorm:"column:c_event_occurred_at"`
	Status          string `gorm:"column:c_status"`
	// PlannedRound 是已规划到第几轮：0 还没到 T−24h，1 已按 T−24h 规划，2 已按 T−2h 规划。
	PlannedRound int    `gorm:"column:c_planned_round"`
	Note         string `gorm:"column:c_note"`
	Result       string `gorm:"column:c_result"`
	CreatedAt    int64  `gorm:"column:c_created_at"`
	UpdatedAt    int64  `gorm:"column:c_updated_at"`
	FinishedAt   int64  `gorm:"column:c_finished_at"`
}

// DelistFlattenRecord 是一轮里一个（空间、组合账户）的下架清仓：只存确定生成的操作号，动作状态从 t_operator_actions 读。
type DelistFlattenRecord struct {
	DelistID         string `gorm:"column:c_delist_id"`
	Round            int    `gorm:"column:c_round"`
	SpaceID          string `gorm:"column:c_space_id"`
	LogicalAccountID string `gorm:"column:c_logical_account_id"`
	ActionID         string `gorm:"column:c_action_id"`
	CreatedAt        int64  `gorm:"column:c_created_at"`
}

const (
	blockedColumns = `c_exchange, c_market_type, c_exchange_symbol, c_scope, c_instrument_id, c_base_asset,
			c_reason, c_delist_id, c_blocked_at`
	delistColumns = `c_delist_id, c_exchange, c_market_type, c_exchange_symbol, c_scope, c_delist_at,
			c_source, c_source_event_id, c_event_occurred_at, c_status, c_planned_round, c_note, c_result,
			c_created_at, c_updated_at, c_finished_at`

	sqlInsertBlockedInstrument = `
		INSERT INTO t_trade_blocked_instruments (` + blockedColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (c_exchange, c_market_type, c_exchange_symbol) DO NOTHING`
	sqlGetBlockedInstrument = `
		SELECT ` + blockedColumns + `
		FROM t_trade_blocked_instruments
		WHERE c_exchange = ? AND c_market_type = ? AND c_exchange_symbol = ?`
	sqlUpdateBlockedInstrument = `
		UPDATE t_trade_blocked_instruments SET c_scope = ?, c_reason = ?, c_delist_id = ?
		WHERE c_exchange = ? AND c_market_type = ? AND c_exchange_symbol = ?`
	sqlDeleteBlockedInstrument = `
		DELETE FROM t_trade_blocked_instruments
		WHERE c_exchange = ? AND c_market_type = ? AND c_exchange_symbol = ?`
	sqlDeletePaperOnlyBlockedInstrument = `
		DELETE FROM t_trade_blocked_instruments
		WHERE c_exchange = ? AND c_market_type = ? AND c_exchange_symbol = ? AND c_scope = 'PAPER_ONLY'`
	sqlListBlockedInstruments = `
		SELECT ` + blockedColumns + `
		FROM t_trade_blocked_instruments
		ORDER BY c_blocked_at, c_exchange, c_market_type, c_exchange_symbol`

	sqlInsertDelist = `
		INSERT INTO t_trade_delists (` + delistColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	sqlDelistBySourceEvent = `SELECT ` + delistColumns + ` FROM t_trade_delists WHERE c_source_event_id = ?`
	sqlGetDelist           = `SELECT ` + delistColumns + ` FROM t_trade_delists WHERE c_delist_id = ?`
	// 不论状态，取同一（标的、范围）事件发生时间最晚的一条，用来挡住乱序到达的旧事件。
	sqlLatestDelist = `
		SELECT ` + delistColumns + ` FROM t_trade_delists
		WHERE c_exchange = ? AND c_market_type = ? AND c_exchange_symbol = ? AND c_scope = ?
		ORDER BY c_event_occurred_at DESC, c_created_at DESC
		LIMIT 1`
	sqlActiveDelist = `
		SELECT ` + delistColumns + ` FROM t_trade_delists
		WHERE c_exchange = ? AND c_market_type = ? AND c_exchange_symbol = ? AND c_scope = ? AND c_status = 'ACTIVE'`
	sqlListActiveDelists = `
		SELECT ` + delistColumns + ` FROM t_trade_delists
		WHERE c_status = 'ACTIVE'
		ORDER BY c_delist_at, c_delist_id`
	sqlListFailedDelistsSince = `
		SELECT ` + delistColumns + ` FROM t_trade_delists
		WHERE c_status = 'FAILED' AND c_finished_at >= ?
		ORDER BY c_finished_at, c_delist_id`
	sqlFinishDelist = `
		UPDATE t_trade_delists SET c_status = ?, c_result = ?, c_finished_at = ?, c_updated_at = ?
		WHERE c_delist_id = ? AND c_status = 'ACTIVE'`
	sqlCancelActiveDelists = `
		UPDATE t_trade_delists SET c_status = 'CANCELED', c_result = ?, c_finished_at = ?, c_updated_at = ?
		WHERE c_exchange = ? AND c_market_type = ? AND c_exchange_symbol = ? AND c_status = 'ACTIVE'`
	sqlPlanDelistRound = `
		UPDATE t_trade_delists SET c_planned_round = ?, c_note = ?, c_updated_at = ?
		WHERE c_delist_id = ? AND c_status = 'ACTIVE' AND c_planned_round < ?`
	sqlUpdateDelistNote = `
		UPDATE t_trade_delists SET c_note = ?, c_updated_at = ?
		WHERE c_delist_id = ? AND c_status = 'ACTIVE'`
	sqlInsertDelistFlatten = `
		INSERT INTO t_trade_delist_flattens (
			c_delist_id, c_round, c_space_id, c_logical_account_id, c_action_id, c_created_at
		) VALUES (?, ?, ?, ?, ?, ?)`
	sqlListDelistFlattens = `
		SELECT c_delist_id, c_round, c_space_id, c_logical_account_id, c_action_id, c_created_at
		FROM t_trade_delist_flattens
		WHERE c_delist_id = ?
		ORDER BY c_round, c_space_id, c_logical_account_id`
)

func upperTrim(value string) string { return strings.ToUpper(strings.TrimSpace(value)) }

// BlockInstrument 在事务里写一行名单；已经在名单里时不改动，返回 created=false。范围为空按 ALL 写。
func (tx *Tx) BlockInstrument(record BlockedInstrumentRecord) (bool, error) {
	record.Exchange, record.MarketType, record.ExchangeSymbol = upperTrim(record.Exchange), upperTrim(record.MarketType), upperTrim(record.ExchangeSymbol)
	record.Reason = strings.TrimSpace(record.Reason)
	if record.Scope == "" {
		record.Scope = DelistScopeAll
	}
	if record.Exchange == "" || record.MarketType == "" || record.ExchangeSymbol == "" || record.Reason == "" {
		return false, fmt.Errorf("%w: incomplete blocked instrument", ErrInvalidRecord)
	}
	if record.BlockedAt <= 0 {
		record.BlockedAt = time.Now().UnixMilli()
	}
	result := tx.db.Exec(sqlInsertBlockedInstrument,
		record.Exchange, record.MarketType, record.ExchangeSymbol, record.Scope,
		record.InstrumentID, record.BaseAsset, record.Reason, strings.TrimSpace(record.DelistID), record.BlockedAt)
	if err := writeError(result.Error); err != nil {
		return false, err
	}
	return result.RowsAffected > 0, nil
}

// GetBlockedInstrument 读一行名单；不在名单里 found=false。
func (tx *Tx) GetBlockedInstrument(exchange, marketType, exchangeSymbol string) (record BlockedInstrumentRecord, found bool, err error) {
	rows := []BlockedInstrumentRecord{}
	if err := tx.db.Raw(sqlGetBlockedInstrument, upperTrim(exchange), upperTrim(marketType), upperTrim(exchangeSymbol)).Scan(&rows).Error; err != nil {
		return BlockedInstrumentRecord{}, false, err
	}
	if len(rows) == 0 {
		return BlockedInstrumentRecord{}, false, nil
	}
	return rows[0], true, nil
}

// UpdateBlockedInstrument 改写一行名单的范围、原因和下架记录。
func (tx *Tx) UpdateBlockedInstrument(record BlockedInstrumentRecord) error {
	return writeError(tx.db.Exec(sqlUpdateBlockedInstrument, record.Scope, record.Reason, record.DelistID,
		upperTrim(record.Exchange), upperTrim(record.MarketType), upperTrim(record.ExchangeSymbol)).Error)
}

// DeletePaperOnlyBlockedInstrument 删掉范围仍是 PAPER_ONLY 的那一行（演练结束）；已被升级为 ALL 的不删。
func (tx *Tx) DeletePaperOnlyBlockedInstrument(exchange, marketType, exchangeSymbol string) (bool, error) {
	result := tx.db.Exec(sqlDeletePaperOnlyBlockedInstrument, upperTrim(exchange), upperTrim(marketType), upperTrim(exchangeSymbol))
	if err := writeError(result.Error); err != nil {
		return false, err
	}
	return result.RowsAffected > 0, nil
}

// UnblockInstrument 在事务里删掉名单行，并把这个标的所有进行中的下架记录（两种范围都算）记为 CANCELED。
// 不在名单里返回 removed=false，不算错误；进行中的记录照样取消。
func (tx *Tx) UnblockInstrument(exchange, marketType, exchangeSymbol, result string, at int64) (removed bool, canceled int64, err error) {
	exchange, marketType, exchangeSymbol = upperTrim(exchange), upperTrim(marketType), upperTrim(exchangeSymbol)
	deleted := tx.db.Exec(sqlDeleteBlockedInstrument, exchange, marketType, exchangeSymbol)
	if err := writeError(deleted.Error); err != nil {
		return false, 0, err
	}
	updated := tx.db.Exec(sqlCancelActiveDelists, result, at, at, exchange, marketType, exchangeSymbol)
	if err := writeError(updated.Error); err != nil {
		return false, 0, err
	}
	return deleted.RowsAffected > 0, updated.RowsAffected, nil
}

// UnblockInstrument 在单独的事务里解除禁止，取消原因写“名单被手工解除”。
func (s *Store) UnblockInstrument(ctx context.Context, exchange, marketType, exchangeSymbol string) (removed bool, err error) {
	err = s.Transaction(ctx, func(tx *Tx) error {
		removed, _, err = tx.UnblockInstrument(exchange, marketType, exchangeSymbol, DelistUnblockedResult, time.Now().UnixMilli())
		return err
	})
	return removed, err
}

// DelistUnblockedResult 是手工解除名单时进行中的下架记录的结束说明。
const DelistUnblockedResult = "名单被手工解除"

// ListBlockedInstruments 按写入时间返回整张下架名单。
func (s *Store) ListBlockedInstruments(ctx context.Context) ([]BlockedInstrumentRecord, error) {
	records := []BlockedInstrumentRecord{}
	if err := s.db.WithContext(ctx).Raw(sqlListBlockedInstruments).Scan(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// InsertDelist 插入一条下架记录。
func (tx *Tx) InsertDelist(record DelistRecord) error {
	return writeError(tx.db.Exec(sqlInsertDelist,
		record.DelistID, record.Exchange, record.MarketType, record.ExchangeSymbol, record.Scope, record.DelistAt,
		record.Source, record.SourceEventID, record.EventOccurredAt, record.Status, record.PlannedRound, record.Note, record.Result,
		record.CreatedAt, record.UpdatedAt, record.FinishedAt).Error)
}

func (tx *Tx) oneDelist(query string, args ...any) (DelistRecord, bool, error) {
	rows := []DelistRecord{}
	if err := tx.db.Raw(query, args...).Scan(&rows).Error; err != nil {
		return DelistRecord{}, false, err
	}
	if len(rows) == 0 {
		return DelistRecord{}, false, nil
	}
	return rows[0], true, nil
}

// FindDelistBySourceEvent 按来源事件 ID 找记录（任何状态），用来挡住重投。
func (tx *Tx) FindDelistBySourceEvent(eventID string) (DelistRecord, bool, error) {
	return tx.oneDelist(sqlDelistBySourceEvent, eventID)
}

// LatestDelist 取（标的、范围）事件发生时间最晚的一条记录，不论状态。
func (tx *Tx) LatestDelist(exchange, marketType, exchangeSymbol, scope string) (DelistRecord, bool, error) {
	return tx.oneDelist(sqlLatestDelist, exchange, marketType, exchangeSymbol, scope)
}

// FindActiveDelist 取（标的、范围）进行中的记录；部分唯一索引保证至多一条。
func (tx *Tx) FindActiveDelist(exchange, marketType, exchangeSymbol, scope string) (DelistRecord, bool, error) {
	return tx.oneDelist(sqlActiveDelist, exchange, marketType, exchangeSymbol, scope)
}

// FinishDelist 把进行中的记录记成终态；记录已结束时返回 false，不改动。
func (tx *Tx) FinishDelist(delistID, status, result string, at int64) (bool, error) {
	updated := tx.db.Exec(sqlFinishDelist, status, result, at, at, delistID)
	if err := writeError(updated.Error); err != nil {
		return false, err
	}
	return updated.RowsAffected > 0, nil
}

// PlanDelistRound 把进行中的记录推进到第 round 轮并插入这一轮的各行；记录已结束或已规划到这一轮时返回 false，不插任何行。
func (tx *Tx) PlanDelistRound(delistID string, round int, note string, at int64, flattens []DelistFlattenRecord) (bool, error) {
	updated := tx.db.Exec(sqlPlanDelistRound, round, note, at, delistID, round)
	if err := writeError(updated.Error); err != nil {
		return false, err
	}
	if updated.RowsAffected == 0 {
		return false, nil
	}
	for _, flatten := range flattens {
		if err := writeError(tx.db.Exec(sqlInsertDelistFlatten,
			delistID, round, flatten.SpaceID, flatten.LogicalAccountID, flatten.ActionID, at).Error); err != nil {
			return false, err
		}
	}
	return true, nil
}

// GetDelist 按 ID 读一条下架记录。
func (s *Store) GetDelist(ctx context.Context, delistID string) (DelistRecord, error) {
	rows := []DelistRecord{}
	if err := s.db.WithContext(ctx).Raw(sqlGetDelist, delistID).Scan(&rows).Error; err != nil {
		return DelistRecord{}, err
	}
	if len(rows) == 0 {
		return DelistRecord{}, gorm.ErrRecordNotFound
	}
	return rows[0], nil
}

// UpdateDelistNote 改写进行中记录的说明（例如查持有者失败的原因）。
func (s *Store) UpdateDelistNote(ctx context.Context, delistID, note string, at int64) error {
	return writeError(s.db.WithContext(ctx).Exec(sqlUpdateDelistNote, note, at, delistID).Error)
}

// ListActiveDelists 返回全部进行中的下架记录，按下架时间排序。
func (s *Store) ListActiveDelists(ctx context.Context) ([]DelistRecord, error) {
	records := []DelistRecord{}
	if err := s.db.WithContext(ctx).Raw(sqlListActiveDelists).Scan(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// ListDelists 按创建时间倒序返回下架记录；activeOnly 时只列进行中的。limit 不大于 0 时不限。
func (s *Store) ListDelists(ctx context.Context, activeOnly bool, limit int) ([]DelistRecord, error) {
	query := `SELECT ` + delistColumns + ` FROM t_trade_delists`
	if activeOnly {
		query += ` WHERE c_status = 'ACTIVE'`
	}
	query += ` ORDER BY c_created_at DESC, c_delist_id DESC`
	if limit <= 0 {
		limit = -1
	}
	query += ` LIMIT ?`
	records := []DelistRecord{}
	if err := s.db.WithContext(ctx).Raw(query, limit).Scan(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// ListFailedDelistsSince 返回在 since（毫秒）之后结束、核对失败的记录。
func (s *Store) ListFailedDelistsSince(ctx context.Context, since int64) ([]DelistRecord, error) {
	records := []DelistRecord{}
	if err := s.db.WithContext(ctx).Raw(sqlListFailedDelistsSince, since).Scan(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// ListDelistFlattens 返回一条记录各轮各组合的行。
func (s *Store) ListDelistFlattens(ctx context.Context, delistID string) ([]DelistFlattenRecord, error) {
	records := []DelistFlattenRecord{}
	if err := s.db.WithContext(ctx).Raw(sqlListDelistFlattens, delistID).Scan(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// FindOperatorAction 按（空间、操作号）读动作；不存在 found=false。
func (s *Store) FindOperatorAction(ctx context.Context, spaceID, actionID string) (OperatorActionRecord, bool, error) {
	action, err := s.GetOperatorAction(ctx, spaceID, actionID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return OperatorActionRecord{}, false, nil
	}
	if err != nil {
		return OperatorActionRecord{}, false, err
	}
	return action, true, nil
}

type blockKey struct{ exchange, marketType, symbol string }

// BlockedSet 是下架名单的匹配视图：执行器、目标接收、清仓、持有者查询都用同一套匹配规则。
// 匹配只看（交易所、市场、交易所代码）与范围，不用名单里存的内部标的 ID 和基础资产，所以以后才开通这个市场的账户也能匹配上。
type BlockedSet struct {
	scopes map[blockKey]string
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
	set := BlockedSet{scopes: make(map[blockKey]string, len(records))}
	for _, record := range records {
		scope := record.Scope
		if scope == "" {
			scope = DelistScopeAll
		}
		set.scopes[blockKey{record.Exchange, record.MarketType, record.ExchangeSymbol}] = scope
	}
	return set
}

func (b BlockedSet) Empty() bool { return len(b.scopes) == 0 }

// Contains 报告（交易所、市场、交易所代码）对这个账户是否在名单里：范围 ALL 对所有账户生效，PAPER_ONLY 只对模拟盘账户生效。
// 交易所取账户的交易所。
func (b BlockedSet) Contains(account TradingAccountRecord, marketType, exchangeSymbol string) bool {
	scope, found := b.scopes[blockKey{account.Exchange, marketType, exchangeSymbol}]
	if !found {
		return false
	}
	return scope == DelistScopeAll || account.ExecutionMode == string(exchange.ExecutionModePaper)
}

// ContainsInstrument 报告一个标的对这个账户是否在名单里。
func (b BlockedSet) ContainsInstrument(account TradingAccountRecord, instrument InstrumentRecord) bool {
	return b.Contains(account, instrument.MarketType, instrument.ExchangeSymbol)
}

// SpotBaseAssets 返回对这个账户生效的名单里 SPOT 条目对应的基础资产：条目在账户标的表（含已下架的）里查 base_asset。
// 只看 SPOT 条目，永续下架不会把同名的现货余额也豁免掉。
func (b BlockedSet) SpotBaseAssets(account TradingAccountRecord, instruments []InstrumentRecord) map[string]struct{} {
	assets := make(map[string]struct{})
	if b.Empty() {
		return assets
	}
	for _, instrument := range instruments {
		if instrument.MarketType == "SPOT" && b.Contains(account, "SPOT", instrument.ExchangeSymbol) {
			assets[instrument.BaseAsset] = struct{}{}
		}
	}
	return assets
}

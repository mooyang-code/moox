package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ReplaySessionRecord 是一次回放的纸面账本会话：初始资金与费用配置固定，之后只随推进更新现金与持仓。
type ReplaySessionRecord struct {
	ReplayID      string
	Status        string
	QuoteAsset    string
	InitialCash   string
	FeeRate       string
	SlippageBPS   string
	Cash          string
	PositionsJSON string
	LastPeriod    int64
	CreatedAt     int64
	UpdatedAt     int64
	ClosedAt      int64
}

// ReplayPeriodRecord 是一期推进的结果（周期记录，同一期重复推进返回同一条）。
type ReplayPeriodRecord struct {
	ReplayID      string
	Period        int64
	Kind          string
	RequestHash   string
	EquityBefore  string
	Equity        string
	Cash          string
	PositionsJSON string
	Fees          string
	Turnover      string
	ResultJSON    string
	RecordedAt    int64
}

// ReplayOrderRecord 是回放账本里的一笔模拟订单（市价单，按回放报价成交）。
type ReplayOrderRecord struct {
	ReplayID          string
	OrderID           string
	Period            int64
	InstrumentID      string
	Side              string
	RequestedQuantity string
	FilledQuantity    string
	Price             string
	Fee               string
	Status            string
	Reason            string
}

type replaySessionRow struct {
	ReplayID      string `gorm:"column:c_replay_id"`
	Status        string `gorm:"column:c_status"`
	QuoteAsset    string `gorm:"column:c_quote_asset"`
	InitialCash   string `gorm:"column:c_initial_cash"`
	FeeRate       string `gorm:"column:c_fee_rate"`
	SlippageBPS   string `gorm:"column:c_slippage_bps"`
	Cash          string `gorm:"column:c_cash"`
	PositionsJSON string `gorm:"column:c_positions_json"`
	LastPeriod    int64  `gorm:"column:c_last_period"`
	CreatedAt     int64  `gorm:"column:c_created_at"`
	UpdatedAt     int64  `gorm:"column:c_updated_at"`
	ClosedAt      int64  `gorm:"column:c_closed_at"`
}

type replayPeriodRow struct {
	ReplayID      string `gorm:"column:c_replay_id"`
	Period        int64  `gorm:"column:c_period"`
	Kind          string `gorm:"column:c_kind"`
	RequestHash   string `gorm:"column:c_request_hash"`
	EquityBefore  string `gorm:"column:c_equity_before"`
	Equity        string `gorm:"column:c_equity"`
	Cash          string `gorm:"column:c_cash"`
	PositionsJSON string `gorm:"column:c_positions_json"`
	Fees          string `gorm:"column:c_fees"`
	Turnover      string `gorm:"column:c_turnover"`
	ResultJSON    string `gorm:"column:c_result_json"`
	RecordedAt    int64  `gorm:"column:c_recorded_at"`
}

type replayOrderRow struct {
	ReplayID          string `gorm:"column:c_replay_id"`
	OrderID           string `gorm:"column:c_order_id"`
	Period            int64  `gorm:"column:c_period"`
	InstrumentID      string `gorm:"column:c_instrument_id"`
	Side              string `gorm:"column:c_side"`
	RequestedQuantity string `gorm:"column:c_requested_quantity"`
	FilledQuantity    string `gorm:"column:c_filled_quantity"`
	Price             string `gorm:"column:c_price"`
	Fee               string `gorm:"column:c_fee"`
	Status            string `gorm:"column:c_status"`
	Reason            string `gorm:"column:c_reason"`
}

// CreateReplaySession 创建回放会话；同一个 replay_id 已经存在时返回已有的会话（created 为 false），配置以先创建的为准。
func (s *Store) CreateReplaySession(ctx context.Context, record ReplaySessionRecord) (ReplaySessionRecord, bool, error) {
	if record.ReplayID == "" || record.InitialCash == "" || record.FeeRate == "" || record.SlippageBPS == "" || record.CreatedAt <= 0 {
		return ReplaySessionRecord{}, false, fmt.Errorf("%w: incomplete replay session", ErrInvalidRecord)
	}
	if record.QuoteAsset == "" {
		record.QuoteAsset = "USDT"
	}
	if record.PositionsJSON == "" {
		record.PositionsJSON = "{}"
	}
	if record.Cash == "" {
		record.Cash = record.InitialCash
	}
	result := s.db.WithContext(ctx).Exec(`
		INSERT INTO t_replay_sessions (
			c_replay_id, c_status, c_quote_asset, c_initial_cash, c_fee_rate, c_slippage_bps,
			c_cash, c_positions_json, c_last_period, c_created_at, c_updated_at
		) VALUES (?, 'open', ?, ?, ?, ?, ?, ?, 0, ?, ?)
		ON CONFLICT(c_replay_id) DO NOTHING
	`, record.ReplayID, record.QuoteAsset, record.InitialCash, record.FeeRate, record.SlippageBPS,
		record.Cash, record.PositionsJSON, record.CreatedAt, record.CreatedAt)
	if err := writeError(result.Error); err != nil {
		return ReplaySessionRecord{}, false, err
	}
	created := result.RowsAffected > 0
	got, err := s.GetReplaySession(ctx, record.ReplayID)
	return got, created, err
}

// GetReplaySession 读取会话。
func (s *Store) GetReplaySession(ctx context.Context, replayID string) (ReplaySessionRecord, error) {
	var row replaySessionRow
	if err := s.db.WithContext(ctx).Table("t_replay_sessions").Where("c_replay_id = ?", replayID).Take(&row).Error; err != nil {
		return ReplaySessionRecord{}, err
	}
	return ReplaySessionRecord(row), nil
}

// ReplayPeriodWrite 是一期推进要原子写入的内容：周期记录、订单，以及会话的新现金与持仓。
type ReplayPeriodWrite struct {
	Period ReplayPeriodRecord
	Orders []ReplayOrderRecord
}

// ErrReplayOutOfOrder 表示推进的周期不晚于已经推进到的周期（同一期重复推进由调用方先按周期记录处理）。
var ErrReplayOutOfOrder = errors.New("trade: replay period is not after the last advanced period")

// WriteReplayPeriod 在一个事务里写入周期记录与订单，并更新会话的现金、持仓与推进位置。会话已结束或周期不是向前推进时拒绝。
func (s *Store) WriteReplayPeriod(ctx context.Context, write ReplayPeriodWrite) error {
	p := write.Period
	if p.ReplayID == "" || p.Period <= 0 || p.Kind == "" || p.RequestHash == "" || p.RecordedAt <= 0 {
		return fmt.Errorf("%w: incomplete replay period", ErrInvalidRecord)
	}
	for name, raw := range map[string]string{"positions": p.PositionsJSON, "result": p.ResultJSON} {
		if raw == "" || !json.Valid([]byte(raw)) {
			return fmt.Errorf("%w: invalid replay period %s", ErrInvalidRecord, name)
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session replaySessionRow
		if err := tx.Table("t_replay_sessions").Where("c_replay_id = ?", p.ReplayID).Take(&session).Error; err != nil {
			return err
		}
		if session.Status != "open" {
			return fmt.Errorf("%w: replay session %s is closed", ErrConflict, p.ReplayID)
		}
		if p.Period <= session.LastPeriod {
			return ErrReplayOutOfOrder
		}
		if err := tx.Exec(`
			INSERT INTO t_replay_periods (
				c_replay_id, c_period, c_kind, c_request_hash, c_equity_before, c_equity, c_cash,
				c_positions_json, c_fees, c_turnover, c_result_json, c_recorded_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, p.ReplayID, p.Period, p.Kind, p.RequestHash, p.EquityBefore, p.Equity, p.Cash,
			p.PositionsJSON, p.Fees, p.Turnover, p.ResultJSON, p.RecordedAt).Error; err != nil {
			return writeError(err)
		}
		for _, order := range write.Orders {
			if err := tx.Exec(`
				INSERT INTO t_replay_orders (
					c_replay_id, c_order_id, c_period, c_instrument_id, c_side, c_requested_quantity,
					c_filled_quantity, c_price, c_fee, c_status, c_reason
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, order.ReplayID, order.OrderID, order.Period, order.InstrumentID, order.Side, order.RequestedQuantity,
				order.FilledQuantity, order.Price, order.Fee, order.Status, order.Reason).Error; err != nil {
				return writeError(err)
			}
		}
		return tx.Exec(`
			UPDATE t_replay_sessions
			SET c_cash = ?, c_positions_json = ?, c_last_period = ?, c_updated_at = ?
			WHERE c_replay_id = ?
		`, p.Cash, p.PositionsJSON, p.Period, p.RecordedAt, p.ReplayID).Error
	})
}

// GetReplayPeriod 读取一期的周期记录；没有时返回 gorm.ErrRecordNotFound。
func (s *Store) GetReplayPeriod(ctx context.Context, replayID string, period int64) (ReplayPeriodRecord, error) {
	var row replayPeriodRow
	if err := s.db.WithContext(ctx).Table("t_replay_periods").Where("c_replay_id = ? AND c_period = ?", replayID, period).Take(&row).Error; err != nil {
		return ReplayPeriodRecord{}, err
	}
	return ReplayPeriodRecord(row), nil
}

// ListReplayPeriods 按周期升序返回 after 之后的周期记录（limit <= 0 取 1000）。
func (s *Store) ListReplayPeriods(ctx context.Context, replayID string, after int64, limit int) ([]ReplayPeriodRecord, error) {
	if limit <= 0 {
		limit = 1000
	}
	var rows []replayPeriodRow
	err := s.db.WithContext(ctx).Table("t_replay_periods").
		Where("c_replay_id = ? AND c_period > ?", replayID, after).Order("c_period").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]ReplayPeriodRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, ReplayPeriodRecord(r))
	}
	return out, nil
}

// ListReplayOrders 返回一期的订单（period 为 0 时返回全部，按周期与订单号排序）。
func (s *Store) ListReplayOrders(ctx context.Context, replayID string, period int64) ([]ReplayOrderRecord, error) {
	var rows []replayOrderRow
	query := s.db.WithContext(ctx).Table("t_replay_orders").Where("c_replay_id = ?", replayID)
	if period > 0 {
		query = query.Where("c_period = ?", period)
	}
	if err := query.Order("c_period, c_order_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ReplayOrderRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, ReplayOrderRecord(r))
	}
	return out, nil
}

// CloseReplaySession 结束会话（保留结果）；重复结束不报错。
func (s *Store) CloseReplaySession(ctx context.Context, replayID string, at int64) error {
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_replay_sessions SET c_status = 'closed', c_closed_at = ?, c_updated_at = ?
		WHERE c_replay_id = ? AND c_status = 'open'
	`, at, at, replayID)
	if err := result.Error; err != nil {
		return err
	}
	if result.RowsAffected == 0 {
		if _, err := s.GetReplaySession(ctx, replayID); err != nil {
			return err
		}
	}
	return nil
}

// DeleteReplaySession 清理一次回放的全部结果（会话、周期记录与订单）。
func (s *Store) DeleteReplaySession(ctx context.Context, replayID string) (bool, error) {
	result := s.db.WithContext(ctx).Exec(`DELETE FROM t_replay_sessions WHERE c_replay_id = ?`, replayID)
	return result.RowsAffected > 0, result.Error
}

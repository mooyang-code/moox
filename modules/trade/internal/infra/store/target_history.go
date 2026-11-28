package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// TargetHistoryRecord 是目标历史表（t_logical_account_target_history）的一行：收到的一个目标与它的换算快照。
// Shadow 为假是被接受的正式目标；为真是试运行中的策略实例发来的影子目标，只换算、记录，不会被执行。
type TargetHistoryRecord struct {
	SpaceID             string
	TargetID            string
	LogicalAccountID    string
	Shadow              bool
	InstanceID          string
	SessionID           string
	SessionStartedAt    int64
	StrategyID          string
	BarEndTime          int64
	ValidUntil          int64
	RequestHash         string
	SignalTime          int64
	WeightsJSON         string
	Equity              string
	EquitySourceTime    int64
	ReferencePricesJSON string
	QuantityTargetsJSON string
	// RecordedAt 是正式目标的接受时刻、影子目标的记录时刻（毫秒）。
	RecordedAt int64
}

type targetHistoryRow struct {
	SpaceID             string `gorm:"column:c_space_id"`
	TargetID            string `gorm:"column:c_target_id"`
	LogicalAccountID    string `gorm:"column:c_logical_account_id"`
	Shadow              bool   `gorm:"column:c_shadow"`
	InstanceID          string `gorm:"column:c_instance_id"`
	SessionID           string `gorm:"column:c_session_id"`
	SessionStartedAt    int64  `gorm:"column:c_session_started_at"`
	StrategyID          string `gorm:"column:c_strategy_id"`
	BarEndTime          int64  `gorm:"column:c_bar_end_time"`
	ValidUntil          int64  `gorm:"column:c_valid_until"`
	RequestHash         string `gorm:"column:c_request_hash"`
	SignalTime          int64  `gorm:"column:c_signal_time"`
	WeightsJSON         string `gorm:"column:c_weights_json"`
	Equity              string `gorm:"column:c_equity"`
	EquitySourceTime    int64  `gorm:"column:c_equity_source_time"`
	ReferencePricesJSON string `gorm:"column:c_reference_prices_json"`
	QuantityTargetsJSON string `gorm:"column:c_quantity_targets_json"`
	RecordedAt          int64  `gorm:"column:c_recorded_at"`
}

// validate 检查一行目标历史是否完整。正式目标只要有换算过数量的标的就必须带权益快照；全部是保持标的的目标没有换算。
func (record *TargetHistoryRecord) validate() error {
	if record.SpaceID == "" || record.TargetID == "" || record.LogicalAccountID == "" || record.RequestHash == "" || record.RecordedAt <= 0 {
		return fmt.Errorf("%w: 目标历史缺少必填字段", ErrInvalidRecord)
	}
	if record.InstanceID == "" || record.SessionID == "" || record.SessionStartedAt <= 0 || record.StrategyID == "" ||
		record.BarEndTime <= 0 || record.ValidUntil <= record.BarEndTime {
		return fmt.Errorf("%w: 目标历史的会话信息不完整", ErrInvalidRecord)
	}
	for name, raw := range map[string]string{"weights": record.WeightsJSON, "reference_prices": record.ReferencePricesJSON, "quantity_targets": record.QuantityTargetsJSON} {
		if raw == "" || !json.Valid([]byte(raw)) {
			return fmt.Errorf("%w: 目标历史的 %s 不是合法 JSON", ErrInvalidRecord, name)
		}
	}
	if record.SignalTime == 0 {
		record.SignalTime = record.BarEndTime
	}
	if record.Shadow {
		return nil
	}
	var quantityTargets []InstrumentQuantity
	if err := json.Unmarshal([]byte(record.QuantityTargetsJSON), &quantityTargets); err != nil {
		return fmt.Errorf("%w: 目标历史的 quantity_targets 无法解析", ErrInvalidRecord)
	}
	for _, target := range quantityTargets {
		if !target.Hold && (record.Equity == "" || record.EquitySourceTime <= 0) {
			return fmt.Errorf("%w: 有换算的目标必须带权益快照", ErrInvalidRecord)
		}
	}
	return nil
}

func (record TargetHistoryRecord) insertArgs() []any {
	return []any{record.SpaceID, record.TargetID, record.LogicalAccountID, record.Shadow,
		record.InstanceID, record.SessionID, record.SessionStartedAt, record.StrategyID,
		record.BarEndTime, record.ValidUntil, record.RequestHash, record.SignalTime, record.WeightsJSON,
		record.Equity, record.EquitySourceTime, record.ReferencePricesJSON,
		record.QuantityTargetsJSON, record.RecordedAt}
}

// InsertTargetHistory 写一行正式目标的历史。
func (tx *Tx) InsertTargetHistory(record TargetHistoryRecord) error {
	record.Shadow = false
	if err := record.validate(); err != nil {
		return err
	}
	return writeError(tx.db.Exec(execInsertTargetHistory, record.insertArgs()...).Error)
}

// GetAcceptedTargetHistory 按目标号取正式目标的历史行，不看影子目标。
func (s *Store) GetAcceptedTargetHistory(ctx context.Context, spaceID, targetID string) (TargetHistoryRecord, error) {
	row, err := findAcceptedTargetHistory(s.db.WithContext(ctx), spaceID, targetID)
	return TargetHistoryRecord(row), err
}

// ListAcceptedTargetHistorySince 按接受时间返回 since 之后的正式目标，最前面再带上 since 之前最近的一条作为比较基准（没有就不带）。
func (s *Store) ListAcceptedTargetHistorySince(ctx context.Context, spaceID, logicalAccountID string, since int64) ([]TargetHistoryRecord, error) {
	var baseline []targetHistoryRow
	if err := s.db.WithContext(ctx).Raw(queryAcceptedTargetBaseline, spaceID, logicalAccountID, since).Scan(&baseline).Error; err != nil {
		return nil, err
	}
	var rows []targetHistoryRow
	if err := s.db.WithContext(ctx).Raw(queryAcceptedTargetsSince, spaceID, logicalAccountID, since).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]TargetHistoryRecord, 0, len(baseline)+len(rows))
	for _, row := range append(baseline, rows...) {
		result = append(result, TargetHistoryRecord(row))
	}
	return result, nil
}

// RecordShadowTarget 记录一个影子目标；同一个 target_id 重复投递时忽略，返回是否新增。
func (s *Store) RecordShadowTarget(ctx context.Context, record TargetHistoryRecord) (bool, error) {
	record.Shadow = true
	if err := record.validate(); err != nil {
		return false, err
	}
	result := s.db.WithContext(ctx).Exec(execInsertShadowTarget, record.insertArgs()...)
	if err := writeError(result.Error); err != nil {
		return false, err
	}
	return result.RowsAffected > 0, nil
}

// ListShadowTargets 按周期时间倒序返回实例最近的影子目标（limit <= 0 时取 100 条）。
func (s *Store) ListShadowTargets(ctx context.Context, spaceID, instanceID string, limit int) ([]TargetHistoryRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []targetHistoryRow
	if err := s.db.WithContext(ctx).Raw(queryShadowTargets, spaceID, instanceID, limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]TargetHistoryRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, TargetHistoryRecord(row))
	}
	return out, nil
}

// DeleteTargetHistoryBefore 删掉最多 limit 条有效期截止（c_valid_until，毫秒）早于 before 的目标历史（正式与影子），
// 返回删掉的条数。一次调用是一条独立语句，由调用方分批调用，批与批之间连接交还给其他读写。
func (s *Store) DeleteTargetHistoryBefore(ctx context.Context, before int64, limit int) (int64, error) {
	result := s.db.WithContext(ctx).Exec(execDeleteTargetHistoryBefore, before, limit)
	if err := writeError(result.Error); err != nil {
		return 0, err
	}
	return result.RowsAffected, nil
}

// findAcceptedTargetHistory 按目标号取正式目标的历史行，没有时返回 gorm.ErrRecordNotFound。
func findAcceptedTargetHistory(db *gorm.DB, spaceID, targetID string) (targetHistoryRow, error) {
	var rows []targetHistoryRow
	if err := db.Raw(queryAcceptedTargetHistory, spaceID, targetID).Scan(&rows).Error; err != nil {
		return targetHistoryRow{}, err
	}
	if len(rows) == 0 {
		return targetHistoryRow{}, gorm.ErrRecordNotFound
	}
	return rows[0], nil
}

// AcceptLogicalAccountTargetWithHistoryLocked 在一个事务里按同一份记录生成当前目标与目标历史并落库，要求调用方已持有组合锁：
// Trade 换算权重时持锁，让成员关系、权益和落库的历史看到的是同一个状态。同一请求哈希的重放是幂等的；同一目标号换了内容是冲突。
func (s *Store) AcceptLogicalAccountTargetWithHistoryLocked(ctx context.Context, history TargetHistoryRecord) (LogicalAccountTargetRecord, bool, error) {
	var current LogicalAccountTargetRecord
	var accepted bool
	err := s.Transaction(ctx, func(tx *Tx) error {
		account, accountErr := tx.GetLogicalAccount(history.SpaceID, history.LogicalAccountID)
		if accountErr != nil {
			return accountErr
		}
		if account.ControlMode != "STRATEGY" {
			return fmt.Errorf("%w: logical account is not strategy controlled", ErrTargetAuthorization)
		}
		// 会话先后（含已关闭）和有效期在历史重放之前判断：旧会话的重复投递不能借已接受过的历史绕过会话与过期的检查。
		// 同一会话的 bar_end_time 先后在历史之后由 AcceptLogicalAccountTarget 判断，被取代之后重投旧目标靠历史确认。
		if blank(history.InstanceID) || blank(history.SessionID) || blank(history.StrategyID) ||
			history.SessionStartedAt <= 0 || history.BarEndTime <= 0 || history.ValidUntil <= history.BarEndTime {
			return fmt.Errorf("%w: incomplete target session contract", ErrInvalidRecord)
		}
		if err := tx.checkTargetSession(account, TargetSession{
			InstanceID: history.InstanceID, SessionID: history.SessionID, StartedAt: history.SessionStartedAt,
		}); err != nil {
			return err
		}
		now := time.Now().UTC().UnixMilli()
		if now >= history.ValidUntil {
			return fmt.Errorf("%w: target validity window [%d,%d), now=%d", ErrTargetExpired, history.BarEndTime, history.ValidUntil, now)
		}
		targets, decodeErr := decodeCurrentTargets(history.QuantityTargetsJSON)
		if decodeErr != nil {
			return decodeErr
		}
		target := LogicalAccountTargetRecord{
			SpaceID:          history.SpaceID,
			LogicalAccountID: history.LogicalAccountID,
			TargetID:         history.TargetID,
			InstanceID:       history.InstanceID,
			SessionID:        history.SessionID,
			SessionStartedAt: history.SessionStartedAt,
			StrategyID:       history.StrategyID,
			BarEndTime:       history.BarEndTime,
			ValidUntil:       history.ValidUntil,
			Targets:          targets,
			Status:           "PENDING",
			AcceptedAt:       history.RecordedAt,
		}
		existing, lookupErr := findAcceptedTargetHistory(tx.db, history.SpaceID, history.TargetID)
		if lookupErr == nil {
			if existing.RequestHash != history.RequestHash {
				return fmt.Errorf("%w: 同一目标的历史请求哈希冲突", ErrConflict)
			}
			current = target
			accepted = false
			return nil
		}
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}
		var err error
		current, accepted, err = tx.AcceptLogicalAccountTarget(target)
		if err != nil || !accepted {
			return err
		}
		return tx.InsertTargetHistory(history)
	})
	return current, accepted, err
}

// 以下 SQL 整条写成常量，不在调用处拼接或内联。
const (
	targetHistoryColumns = `c_space_id, c_target_id, c_logical_account_id, c_shadow,
			c_instance_id, c_session_id, c_session_started_at, c_strategy_id,
			c_bar_end_time, c_valid_until, c_request_hash, c_signal_time, c_weights_json,
			c_equity, c_equity_source_time, c_reference_prices_json,
			c_quantity_targets_json, c_recorded_at`
	execInsertTargetHistory = `INSERT INTO t_logical_account_target_history (` + targetHistoryColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	execInsertShadowTarget = execInsertTargetHistory + `
		ON CONFLICT(c_space_id, c_target_id) DO NOTHING`
	queryAcceptedTargetHistory = `SELECT ` + targetHistoryColumns + ` FROM t_logical_account_target_history
		WHERE c_space_id = ? AND c_target_id = ? AND c_shadow = 0`
	queryAcceptedTargetBaseline = `SELECT ` + targetHistoryColumns + ` FROM t_logical_account_target_history
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_recorded_at <= ? AND c_shadow = 0
		ORDER BY c_recorded_at DESC LIMIT 1`
	queryAcceptedTargetsSince = `SELECT ` + targetHistoryColumns + ` FROM t_logical_account_target_history
		WHERE c_space_id = ? AND c_logical_account_id = ? AND c_recorded_at > ? AND c_shadow = 0
		ORDER BY c_recorded_at`
	queryShadowTargets = `SELECT ` + targetHistoryColumns + ` FROM t_logical_account_target_history
		WHERE c_space_id = ? AND c_instance_id = ? AND c_shadow = 1
		ORDER BY c_bar_end_time DESC LIMIT ?`
	// 用 rowid 子查询限制每批行数：SQLite 默认不支持 DELETE ... LIMIT。
	execDeleteTargetHistoryBefore = `DELETE FROM t_logical_account_target_history WHERE rowid IN (
			SELECT rowid FROM t_logical_account_target_history WHERE c_valid_until < ? LIMIT ?
		)`
)

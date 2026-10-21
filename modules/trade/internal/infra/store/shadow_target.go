package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// ShadowTargetRecord 是预热中的策略实例发来的影子目标：完成了权重到数量的换算，但不会被执行。
type ShadowTargetRecord struct {
	SpaceID             string
	TargetID            string
	LogicalAccountID    string
	InstanceID          string
	SessionID           string
	StrategyID          string
	BarEndTime          int64
	ValidUntil          int64
	WeightsJSON         string
	Equity              string
	EquitySourceTime    int64
	ReferencePricesJSON string
	QuantityTargetsJSON string
	RecordedAt          int64
}

type shadowTargetRow struct {
	SpaceID             string `gorm:"column:c_space_id"`
	TargetID            string `gorm:"column:c_target_id"`
	LogicalAccountID    string `gorm:"column:c_logical_account_id"`
	InstanceID          string `gorm:"column:c_instance_id"`
	SessionID           string `gorm:"column:c_session_id"`
	StrategyID          string `gorm:"column:c_strategy_id"`
	BarEndTime          int64  `gorm:"column:c_bar_end_time"`
	ValidUntil          int64  `gorm:"column:c_valid_until"`
	WeightsJSON         string `gorm:"column:c_weights_json"`
	Equity              string `gorm:"column:c_equity"`
	EquitySourceTime    int64  `gorm:"column:c_equity_source_time"`
	ReferencePricesJSON string `gorm:"column:c_reference_prices_json"`
	QuantityTargetsJSON string `gorm:"column:c_quantity_targets_json"`
	RecordedAt          int64  `gorm:"column:c_recorded_at"`
}

// RecordShadowTarget 记录一个影子目标；同一个 target_id 重复投递时忽略，返回是否新增。
func (s *Store) RecordShadowTarget(ctx context.Context, record ShadowTargetRecord) (bool, error) {
	if record.SpaceID == "" || record.TargetID == "" || record.LogicalAccountID == "" || record.InstanceID == "" ||
		record.SessionID == "" || record.StrategyID == "" || record.BarEndTime <= 0 || record.RecordedAt <= 0 {
		return false, fmt.Errorf("%w: incomplete shadow target", ErrInvalidRecord)
	}
	for name, raw := range map[string]string{"weights": record.WeightsJSON, "reference_prices": record.ReferencePricesJSON, "quantity_targets": record.QuantityTargetsJSON} {
		if raw == "" || !json.Valid([]byte(raw)) {
			return false, fmt.Errorf("%w: invalid shadow target %s", ErrInvalidRecord, name)
		}
	}
	result := s.db.WithContext(ctx).Exec(`
		INSERT INTO t_shadow_targets (
			c_space_id, c_target_id, c_logical_account_id, c_instance_id, c_session_id, c_strategy_id,
			c_bar_end_time, c_valid_until, c_weights_json, c_equity, c_equity_source_time,
			c_reference_prices_json, c_quantity_targets_json, c_recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_target_id) DO NOTHING
	`, record.SpaceID, record.TargetID, record.LogicalAccountID, record.InstanceID, record.SessionID, record.StrategyID,
		record.BarEndTime, record.ValidUntil, record.WeightsJSON, record.Equity, record.EquitySourceTime,
		record.ReferencePricesJSON, record.QuantityTargetsJSON, record.RecordedAt)
	if err := writeError(result.Error); err != nil {
		return false, err
	}
	return result.RowsAffected > 0, nil
}

// ListShadowTargets 按周期时间倒序返回实例最近的影子目标（limit <= 0 时取 100 条）。
func (s *Store) ListShadowTargets(ctx context.Context, spaceID, instanceID string, limit int) ([]ShadowTargetRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows []shadowTargetRow
	err := s.db.WithContext(ctx).Table("t_shadow_targets").
		Where("c_space_id = ? AND c_instance_id = ?", spaceID, instanceID).
		Order("c_bar_end_time DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]ShadowTargetRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, ShadowTargetRecord(r))
	}
	return out, nil
}

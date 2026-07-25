package store

import (
	"context"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"gorm.io/gorm"
)

func (s *Store) GetBinding(ctx context.Context, bindingID string) (domain.Binding, error) {
	var binding domain.Binding
	err := s.db.WithContext(ctx).Where("c_binding_id=?", bindingID).First(&binding).Error
	return binding, err
}

func (s *Store) CreateBinding(ctx context.Context, binding domain.Binding) error {
	return s.db.WithContext(ctx).Create(&binding).Error
}

func (s *Store) CreateExecutionBinding(ctx context.Context, binding domain.ExecutionBinding) error {
	return s.db.WithContext(ctx).Create(&binding).Error
}

func (s *Store) GetState(ctx context.Context, bindingID string) (domain.State, error) {
	var state domain.State
	err := s.db.WithContext(ctx).Where("c_binding_id=?", bindingID).First(&state).Error
	return state, err
}

func (s *Store) GetRun(ctx context.Context, runID string) (domain.StrategyRun, error) {
	var run domain.StrategyRun
	err := s.db.WithContext(ctx).Where("c_run_id=?", runID).First(&run).Error
	return run, err
}

func (s *Store) GetRunMetrics(ctx context.Context, runID string) (domain.RunMetrics, error) {
	var metrics domain.RunMetrics
	err := s.db.WithContext(ctx).Where("c_run_id=?", runID).First(&metrics).Error
	return metrics, err
}

func (s *Store) FindAudit(ctx context.Context, operationID string) (domain.OperationAudit, error) {
	var audit domain.OperationAudit
	err := s.db.WithContext(ctx).Where("c_operation_id=?", operationID).First(&audit).Error
	return audit, err
}

func (s *Store) WriteAudit(ctx context.Context, audit domain.OperationAudit) error {
	return s.db.WithContext(ctx).Create(&audit).Error
}

func (s *Store) SetExecutionMode(ctx context.Context, binding domain.Binding, mode, channelID, capitalAmount, quoteAsset string, audit domain.OperationAudit) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing struct {
			Mode string `gorm:"column:c_mode"`
		}
		if err := tx.Table("t_strategy_execution_bindings").Select("c_mode").Where("c_group_id=?", binding.GroupID).First(&existing).Error; err != nil {
			return err
		}
		updates := map[string]any{"c_mode": mode}
		if mode == "paper" || mode == "live" {
			updates["c_channel_id"] = channelID
			updates["c_capital_amount"] = capitalAmount
			updates["c_quote_asset"] = quoteAsset
		}
		if err := tx.Table("t_strategy_execution_bindings").Where("c_group_id=?", binding.GroupID).Updates(updates).Error; err != nil {
			return err
		}
		audit.OldValue = existing.Mode
		audit.NewValue = mode
		return tx.Create(&audit).Error
	})
}

func (s *Store) SetBindingStatus(ctx context.Context, binding domain.Binding, status string, audit domain.OperationAudit) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&domain.Binding{}).Where("c_binding_id=?", binding.BindingID).Update("c_status", status).Error; err != nil {
			return err
		}
		audit.OldValue = binding.Status
		audit.NewValue = status
		return tx.Create(&audit).Error
	})
}

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"gorm.io/gorm"
)

var ErrStateConflict = errors.New("strategy: state conflict")
var ErrIdempotencyConflict = errors.New("strategy: idempotency conflict")

type Repository struct{ DB *gorm.DB }

func New(db *gorm.DB) *Repository { return &Repository{DB: db} }
func (r *Repository) SaveDefinition(ctx context.Context, d domain.StrategyDefinition) error {
	// Strategy versions are immutable. A database conflict is intentionally
	// surfaced to the registry service, which compares the existing hash.
	return r.DB.WithContext(ctx).Create(&d).Error
}
func (r *Repository) GetDefinition(ctx context.Context, id, version string) (domain.StrategyDefinition, error) {
	var d domain.StrategyDefinition
	err := r.DB.WithContext(ctx).Where("c_strategy_id=? AND c_version=?", id, version).First(&d).Error
	return d, err
}
func (r *Repository) GetState(ctx context.Context, binding string) (domain.State, error) {
	var s domain.State
	err := r.DB.WithContext(ctx).Where("c_binding_id=?", binding).First(&s).Error
	return s, err
}
func (r *Repository) Commit(ctx context.Context, task domain.Task, out domain.Output, inputHash string) error {
	return r.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing struct {
			RunID     string `gorm:"column:c_run_id"`
			InputHash string `gorm:"column:c_input_hash"`
		}
		lookup := tx.Table("t_strategy_runs").Where("c_binding_id=? AND c_strategy_version=? AND c_trigger_bar_time=? AND c_namespace=?", task.BindingID, task.Version, task.TriggerBarTime, task.Namespace).First(&existing)
		if lookup.Error == nil {
			if existing.InputHash != inputHash {
				return ErrIdempotencyConflict
			}
			return nil
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		var state domain.State
		if err := tx.Where("c_binding_id=?", task.BindingID).First(&state).Error; err != nil {
			return err
		}
		if state.Revision != task.PreviousState.Revision || state.StrategyVersion != task.Version {
			return ErrStateConflict
		}
		raw, err := jsonMarshal(out)
		if err != nil {
			return err
		}
		run := map[string]any{"c_run_id": task.RunID, "c_binding_id": task.BindingID, "c_strategy_version": task.Version, "c_namespace": task.Namespace, "c_trigger_bar_time": task.TriggerBarTime, "c_data_revision": task.DataRevision, "c_input_hash": inputHash, "c_previous_state_revision": state.Revision, "c_status": "accepted", "c_action": out.Action, "c_output_json": string(raw)}
		if err := tx.Table("t_strategy_runs").Create(run).Error; err != nil {
			var concurrent struct {
				InputHash string `gorm:"column:c_input_hash"`
			}
			if lookupErr := tx.Table("t_strategy_runs").Where("c_binding_id=? AND c_strategy_version=? AND c_trigger_bar_time=? AND c_namespace=?", task.BindingID, task.Version, task.TriggerBarTime, task.Namespace).First(&concurrent).Error; lookupErr == nil {
				if concurrent.InputHash == inputHash {
					return nil
				}
				return ErrIdempotencyConflict
			}
			return err
		}
		next, err := jsonMarshal(out.NextState)
		if err != nil {
			return err
		}
		res := tx.Model(&domain.State{}).Where("c_binding_id=? AND c_state_revision=? AND c_strategy_version=?", task.BindingID, state.Revision, task.Version).Updates(map[string]any{"c_state_revision": state.Revision + 1, "c_state_json": string(next), "c_last_run_id": task.RunID})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrStateConflict
		}
		return tx.Table("t_strategy_outbox").Create(map[string]any{"c_message_id": task.RunID, "c_topic": "moox.strategy.action.accepted.v1", "c_payload": raw}).Error
	})
}
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

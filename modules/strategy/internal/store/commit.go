package store

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) Commit(ctx context.Context, task domain.Task, output domain.Output, inputHash string) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		err = s.commitOnce(ctx, task, output, inputHash)
		if !isRetryableLock(err) || attempt == 3 {
			observeStrategyCommit(task, err)
			return err
		}
		select {
		case <-ctx.Done():
			observeStrategyCommit(task, ctx.Err())
			return ctx.Err()
		case <-time.After(time.Duration(1<<attempt) * time.Millisecond):
		}
	}
	observeStrategyCommit(task, err)
	return err
}

func observeStrategyCommit(task domain.Task, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	_ = report.ObserveModuleRun("strategy", "target_commit", result, "strategy-targets", time.Now())
	watermark, parseErr := time.Parse(time.RFC3339Nano, task.TriggerBarTime)
	if parseErr == nil {
		_ = report.ObserveModuleInputWatermark("strategy", "target_commit", "strategy-targets", watermark)
		if err == nil {
			_ = report.ObserveModuleWatermark("strategy", "target_commit", "strategy-targets", watermark)
		}
	}
}

func (s *Store) commitOnce(ctx context.Context, task domain.Task, output domain.Output, inputHash string) error {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		raw, err := jsonMarshal(output)
		if err != nil {
			return err
		}
		run := map[string]any{"c_run_id": task.RunID, "c_binding_id": task.BindingID, "c_strategy_version": task.Version, "c_namespace": task.Namespace, "c_trigger_bar_time": task.TriggerBarTime, "c_data_revision": task.DataRevision, "c_input_hash": inputHash, "c_previous_state_revision": state.Revision, "c_status": "accepted", "c_action": output.Action, "c_output_json": string(raw)}
		inserted := tx.Table("t_strategy_runs").Clauses(clause.OnConflict{DoNothing: true}).Create(run)
		if inserted.Error != nil {
			return inserted.Error
		}
		if inserted.RowsAffected == 0 {
			var concurrent struct {
				InputHash string `gorm:"column:c_input_hash"`
			}
			if lookupErr := tx.Table("t_strategy_runs").Where("c_binding_id=? AND c_strategy_version=? AND c_trigger_bar_time=? AND c_namespace=?", task.BindingID, task.Version, task.TriggerBarTime, task.Namespace).First(&concurrent).Error; lookupErr != nil {
				return lookupErr
			}
			if concurrent.InputHash == inputHash {
				return nil
			}
			return ErrIdempotencyConflict
		}
		next, err := jsonMarshal(output.NextState)
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
		return tx.Table("t_strategy_outbox").Create(map[string]any{"c_message_id": task.RunID, "c_topic": "moox.strategy.output.accepted.v1", "c_payload": raw}).Error
	})
}

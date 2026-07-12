package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"sync"
	"time"
)

var ErrStateConflict = errors.New("strategy: state conflict")
var ErrIdempotencyConflict = errors.New("strategy: idempotency conflict")

type Repository struct {
	db       *gorm.DB
	commitMu sync.Mutex
}

func New(db *gorm.DB) *Repository { return &Repository{db: db} }
func (r *Repository) DB() *gorm.DB {
	if r == nil {
		return nil
	}
	return r.db
}
func (r *Repository) SaveDefinition(ctx context.Context, d domain.StrategyDefinition) error {
	// Strategy versions are immutable. A database conflict is intentionally
	// surfaced to the registry service, which compares the existing hash.
	return r.db.WithContext(ctx).Create(&d).Error
}
func (r *Repository) GetDefinition(ctx context.Context, id, version string) (domain.StrategyDefinition, error) {
	var d domain.StrategyDefinition
	err := r.db.WithContext(ctx).Where("c_strategy_id=? AND c_version=?", id, version).First(&d).Error
	return d, err
}

// GetBinding returns the immutable execution configuration for a binding. A
// run must resolve its strategy version through the binding instead of letting
// callers choose a different version accidentally.
func (r *Repository) GetBinding(ctx context.Context, id string) (domain.Binding, error) {
	var b domain.Binding
	err := r.db.WithContext(ctx).Where("c_binding_id=?", id).First(&b).Error
	return b, err
}
func (r *Repository) GetState(ctx context.Context, binding string) (domain.State, error) {
	var s domain.State
	err := r.db.WithContext(ctx).Where("c_binding_id=?", binding).First(&s).Error
	return s, err
}
func (r *Repository) Commit(ctx context.Context, task domain.Task, out domain.Output, inputHash string) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		err = r.commitOnce(ctx, task, out, inputHash)
		if !isRetryableLock(err) || attempt == 3 {
			return err
		}
		delay := time.Duration(1<<attempt) * time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return err
}

func (r *Repository) commitOnce(ctx context.Context, task domain.Task, out domain.Output, inputHash string) error {
	r.commitMu.Lock()
	defer r.commitMu.Unlock()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

func isRetryableLock(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "database table is locked") || strings.Contains(message, "deadlocked")
}

package store

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
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
		spaceID := task.SpaceID
		if spaceID == "" {
			spaceID = "moox_system"
		}
		occurredAt, parseErr := time.Parse(time.RFC3339Nano, task.TriggerBarTime)
		if parseErr != nil {
			occurredAt = time.Now().UTC()
		}
		if output.Action != domain.ActionRebalance {
			return nil
		}
		registry, err := events.DefaultRegistry()
		if err != nil {
			return err
		}
		var binding domain.Binding
		if err := tx.Where("c_binding_id=?", task.BindingID).First(&binding).Error; err != nil {
			return err
		}
		var executions []domain.ExecutionBinding
		if err := tx.Where("c_group_id=? AND c_status='enabled'", binding.GroupID).Find(&executions).Error; err != nil {
			return err
		}
		for _, execution := range executions {
			if execution.Mode == "observe" {
				continue
			}
			capital, ok := new(big.Rat).SetString(execution.CapitalAmount)
			if (execution.Mode != "paper" && execution.Mode != "live") ||
				strings.TrimSpace(execution.AccountID) == "" || strings.TrimSpace(execution.ChannelID) == "" ||
				!ok || capital.Sign() <= 0 || strings.TrimSpace(execution.QuoteAsset) == "" {
				return fmt.Errorf("invalid execution binding %q", execution.ExecutionBindingID)
			}
			commandSequence, err := nextExecutionCommandSequence(tx, execution.ExecutionBindingID)
			if err != nil {
				return err
			}
			eventID := task.RunID + ":rebalance:" + execution.ExecutionBindingID
			payload := &tradeeventpb.RebalanceRequested{
				RequestId: eventID, StrategyRunId: task.RunID, ExecutionBindingId: execution.ExecutionBindingID,
				AccountId: execution.AccountID, ChannelId: execution.ChannelID, Mode: execution.Mode,
				DataRevision: task.DataRevision, CapitalAmount: execution.CapitalAmount, QuoteAsset: execution.QuoteAsset,
				CommandSequence: commandSequence,
			}
			for _, target := range output.Targets {
				symbol := target.Symbol
				if symbol == "" {
					symbol = target.InstrumentID
				}
				marketType := target.MarketType
				if marketType == "" {
					marketType = "spot"
				}
				payload.Targets = append(payload.Targets, &tradeeventpb.RebalanceTarget{
					InstrumentId: target.InstrumentID, Symbol: symbol,
					MarketType: marketType, TargetWeight: target.TargetWeight,
				})
			}
			eventData, err := registry.MarshalMessage(events.TradeRebalanceRequested, payload, events.PublishOptions{
				EventID: eventID, OccurredAt: occurredAt.UTC(), SpaceID: spaceID, SubjectID: execution.ExecutionBindingID,
			})
			if err != nil {
				return err
			}
			if err := tx.Table("t_strategy_outbox").Create(map[string]any{"c_message_id": eventID, "c_event_data": eventData}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func nextExecutionCommandSequence(tx *gorm.DB, executionBindingID string) (uint64, error) {
	result := tx.Exec(`
		INSERT INTO t_strategy_command_sequences(c_execution_binding_id,c_last_sequence)
		VALUES(?,1)
		ON CONFLICT(c_execution_binding_id) DO UPDATE SET
			c_last_sequence=t_strategy_command_sequences.c_last_sequence+1,
			c_mtime=CURRENT_TIMESTAMP
	`, executionBindingID)
	if result.Error != nil {
		return 0, fmt.Errorf("advance command sequence for %q: %w", executionBindingID, result.Error)
	}
	var sequence int64
	if err := tx.Table("t_strategy_command_sequences").
		Select("c_last_sequence").
		Where("c_execution_binding_id=?", executionBindingID).
		Scan(&sequence).Error; err != nil {
		return 0, fmt.Errorf("read command sequence for %q: %w", executionBindingID, err)
	}
	if sequence <= 0 {
		return 0, fmt.Errorf("command sequence for %q is invalid", executionBindingID)
	}
	return uint64(sequence), nil
}

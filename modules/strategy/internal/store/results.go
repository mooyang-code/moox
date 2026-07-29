package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"gorm.io/gorm"
)

var ErrLogicalResultConflict = errors.New("strategy result logical retry conflicts with existing input")
var ErrRunnerNotEnabled = errors.New("strategy runner must be enabled to accept a result")

type CommitResultRequest struct {
	Result domain.StrategyResult
	Output domain.Output
}

type CommitResultOutcome struct {
	Result  domain.StrategyResult
	Created bool
}

type ResultFilter struct {
	RunnerID string
}

func (s *Store) CommitResult(
	ctx context.Context,
	request CommitResultRequest,
) (CommitResultOutcome, error) {
	if err := validateCommitResultRequest(request); err != nil {
		return CommitResultOutcome{}, err
	}
	var outcome CommitResultOutcome
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, err := getLogicalResult(tx, request.Result)
		if err == nil {
			if existing.InputHash != request.Result.InputHash {
				return ErrLogicalResultConflict
			}
			outcome = CommitResultOutcome{Result: existing}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var runner runnerRow
		if err := tx.Table("t_strategy_runners").
			Where("runner_id = ?", request.Result.RunnerID).
			Take(&runner).Error; err != nil {
			return err
		}
		if runner.StrategyID != request.Result.StrategyID {
			return errors.New("strategy result does not match runner strategy")
		}
		if domain.RunnerStatus(runner.Status) != domain.RunnerStatusEnabled {
			return ErrRunnerNotEnabled
		}

		result := request.Result
		outputJSON, targetsJSON, err := encodeCommittedOutput(request.Output)
		if err != nil {
			return err
		}
		result.OutputJSON = outputJSON
		nextSequence := runner.CommandSequence
		if result.Action == domain.ActionRebalance {
			nextSequence++
			result.CommandSequence = &nextSequence
		} else {
			result.CommandSequence = nil
		}
		if err := insertResult(tx, result); err != nil {
			return err
		}

		currentTargetsJSON := runner.CurrentTargetsJSON
		if result.Action == domain.ActionRebalance {
			currentTargetsJSON = string(targetsJSON)
		}
		if err := tx.Exec(`
			UPDATE t_strategy_runners
			SET current_targets_json = ?, command_sequence = ?, last_result_id = ?,
			    last_success_at = ?, last_error = NULL, updated_at = ?
			WHERE runner_id = ?
		`, currentTargetsJSON, nextSequence, result.ID, result.CreatedAt.UTC().UnixMilli(),
			result.CreatedAt.UTC().UnixMilli(), result.RunnerID).Error; err != nil {
			return err
		}

		if result.Action == domain.ActionRebalance && runner.LogicalAccountID.Valid {
			eventData, err := marshalTargetEvent(result, runner, request.Output.Targets)
			if err != nil {
				return err
			}
			if err := tx.Exec(`
				INSERT INTO t_strategy_outbox (message_id, event_data, created_at)
				VALUES (?, ?, ?)
			`, result.ID, eventData, result.CreatedAt.UTC().UnixMilli()).Error; err != nil {
				return err
			}
		}
		outcome = CommitResultOutcome{Result: result, Created: true}
		return nil
	})
	return outcome, err
}

func (s *Store) RecordRunnerFailure(
	ctx context.Context,
	runnerID string,
	failure error,
	at time.Time,
) error {
	if strings.TrimSpace(runnerID) == "" || failure == nil {
		return errors.New("runner id and failure are required")
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_strategy_runners
		SET last_error = ?, updated_at = ?
		WHERE runner_id = ?
	`, failure.Error(), at.UTC().UnixMilli(), runnerID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) GetResult(ctx context.Context, resultID string) (domain.StrategyResult, error) {
	var row resultRow
	err := s.db.WithContext(ctx).Table("t_strategy_results").
		Where("result_id = ?", resultID).
		Take(&row).Error
	return row.domain(), err
}

func (s *Store) ListResults(
	ctx context.Context,
	filter ResultFilter,
) ([]domain.StrategyResult, error) {
	query := s.db.WithContext(ctx).Table("t_strategy_results")
	if filter.RunnerID != "" {
		query = query.Where("runner_id = ?", filter.RunnerID)
	}
	var rows []resultRow
	if err := query.Order("created_at DESC, result_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	results := make([]domain.StrategyResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, row.domain())
	}
	return results, nil
}

func validateCommitResultRequest(request CommitResultRequest) error {
	result := request.Result
	if strings.TrimSpace(result.ID) == "" ||
		strings.TrimSpace(result.RunnerID) == "" ||
		strings.TrimSpace(result.StrategyID) == "" ||
		strings.TrimSpace(result.Namespace) == "" ||
		strings.TrimSpace(result.InputHash) == "" ||
		result.TriggerBarTime.IsZero() ||
		result.CreatedAt.IsZero() {
		return errors.New("strategy result identity is incomplete")
	}
	if result.Action != request.Output.Action ||
		(result.Action != domain.ActionHold && result.Action != domain.ActionRebalance) {
		return errors.New("strategy result action is invalid or inconsistent")
	}
	return nil
}

func getLogicalResult(db *gorm.DB, result domain.StrategyResult) (domain.StrategyResult, error) {
	var row resultRow
	err := db.Table("t_strategy_results").
		Where(
			"runner_id = ? AND strategy_id = ? AND namespace = ? AND trigger_bar_time = ?",
			result.RunnerID,
			result.StrategyID,
			result.Namespace,
			result.TriggerBarTime.UTC().UnixMilli(),
		).
		Take(&row).Error
	return row.domain(), err
}

func insertResult(db *gorm.DB, result domain.StrategyResult) error {
	return db.Exec(`
		INSERT INTO t_strategy_results (
			result_id, runner_id, strategy_id, trigger_bar_time, namespace,
			input_hash, action, output_json, command_sequence, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		result.ID, result.RunnerID, result.StrategyID, result.TriggerBarTime.UTC().UnixMilli(),
		result.Namespace, result.InputHash, result.Action, string(result.OutputJSON),
		int64Value(result.CommandSequence), result.CreatedAt.UTC().UnixMilli(),
	).Error
}

func encodeCommittedOutput(
	output domain.Output,
) (json.RawMessage, json.RawMessage, error) {
	targets := output.Targets
	if targets == nil {
		targets = []domain.InstrumentTarget{}
	}
	targetsJSON, err := json.Marshal(targets)
	if err != nil {
		return nil, nil, fmt.Errorf("encode strategy targets: %w", err)
	}
	value := struct {
		Targets   []domain.InstrumentTarget `json:"targets"`
		DebugInfo map[string]any            `json:"debug_info,omitempty"`
	}{
		Targets:   targets,
		DebugInfo: output.DebugInfo,
	}
	outputJSON, err := json.Marshal(value)
	if err != nil {
		return nil, nil, fmt.Errorf("encode strategy result output: %w", err)
	}
	return outputJSON, targetsJSON, nil
}

func marshalTargetEvent(
	result domain.StrategyResult,
	runner runnerRow,
	targets []domain.InstrumentTarget,
) ([]byte, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	payloadTargets := make([]*tradeeventpb.InstrumentTarget, 0, len(targets))
	for _, target := range targets {
		payloadTargets = append(payloadTargets, &tradeeventpb.InstrumentTarget{
			InstrumentId: target.InstrumentID,
			Quantity:     target.Quantity,
		})
	}
	payload := &tradeeventpb.LogicalAccountTargetRequested{
		TargetId:         result.ID,
		RunnerId:         result.RunnerID,
		LogicalAccountId: runner.LogicalAccountID.String,
		CommandSequence:  *result.CommandSequence,
		Targets:          payloadTargets,
	}
	return registry.MarshalMessage(
		events.LogicalAccountTargetRequested,
		payload,
		events.PublishOptions{
			EventID: result.ID, OccurredAt: result.CreatedAt.UTC(),
			SpaceID: runner.SpaceID, SubjectID: runner.LogicalAccountID.String,
		},
	)
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"gorm.io/gorm"
)

var ErrLogicalResultConflict = errors.New("strategy result logical key is invalid")
var ErrRunnerNotEnabled = errors.New("strategy runner must be enabled to accept an evaluation")

type CommitEvaluationRequest struct {
	Result          domain.StrategyResult
	Evaluation      domain.Evaluation
	OwnerGeneration int64
}

type CommitEvaluationOutcome struct {
	Result  domain.StrategyResult
	Created bool
}

// CommitEvaluation atomically stores the latest result for a runner/strategy/
// period and, for a rebalance, advances the runner and appends one outbox
// event. A changed input hash is deliberately latest-wins for the same period.
func (s *Store) CommitEvaluation(ctx context.Context, request CommitEvaluationRequest) (CommitEvaluationOutcome, error) {
	if err := validateCommitEvaluationRequest(request); err != nil {
		return CommitEvaluationOutcome{}, err
	}
	var outcome CommitEvaluationOutcome
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var runner runnerRow
		if err := tx.Table("t_strategy_runners").Where("runner_id = ?", request.Result.RunnerID).Take(&runner).Error; err != nil {
			return err
		}
		if runner.StrategyID != request.Result.StrategyID {
			return errors.New("strategy evaluation does not match runner strategy")
		}
		if domain.RunnerStatus(runner.Status) != domain.RunnerStatusEnabled {
			return ErrRunnerNotEnabled
		}
		// A claim/rebind can advance the Trade owner lifecycle while the local
		// Strategy snapshot still points at the previous result. Treat the next
		// evaluation as a rebalance even when its weights are unchanged; a HOLD
		// would not recreate the target that Trade correctly fenced away.
		forceRebalance := false
		if request.OwnerGeneration > 0 && runner.LastResultID.Valid {
			var last resultRow
			if err := tx.Table("t_strategy_results").Where("result_id = ?", runner.LastResultID.String).Take(&last).Error; err == nil {
				forceRebalance = resultOwnerGeneration(last) != request.OwnerGeneration
			}
		}
		if runner.LastResultID.Valid {
			var last resultRow
			if err := tx.Table("t_strategy_results").Where("result_id = ?", runner.LastResultID.String).Take(&last).Error; err == nil && request.Result.PeriodTime.Before(time.UnixMilli(last.PeriodTime)) {
				return ErrLogicalResultConflict
			}
		}

		var existing resultRow
		replacingExisting := false
		existingErr := tx.Table("t_strategy_results").Where(
			"runner_id = ? AND strategy_id = ? AND period_time = ?",
			request.Result.RunnerID, request.Result.StrategyID, request.Result.PeriodTime.UTC().UnixMilli(),
		).Order("created_at DESC, result_id DESC").Take(&existing).Error
		// Same-hash is idempotent only when the existing result is still the
		// runner's current result. A disabled runner may be rebound to another
		// Strategy or LogicalAccount and then switched back; in that case the
		// historical row must be replaced so a fresh target event is emitted for
		// the new binding instead of being silently swallowed.
		if existingErr == nil && existing.InputHash == request.Result.InputHash &&
			runner.LastResultID.Valid && runner.LastResultID.String == existing.ID &&
			(!forceRebalance && resultOwnerGeneration(existing) == request.OwnerGeneration) {
			outcome = CommitEvaluationOutcome{Result: existing.domain(), Created: false}
			return nil
		}
		if existingErr != nil && !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return existingErr
		}
		if existingErr == nil {
			replacingExisting = true
		}

		result := request.Result
		evaluation := request.Evaluation
		if forceRebalance && result.Action == domain.ActionHold {
			result.Action = domain.ActionRebalance
			evaluation.Action = domain.ActionRebalance
		}
		var encodeErr error
		result.TargetsJSON, result.DebugInfoJSON, encodeErr = encodeEvaluation(evaluation)
		if encodeErr != nil {
			return encodeErr
		}
		nextSequence := runner.CommandSequence
		if result.Action == domain.ActionRebalance {
			nextSequence++
			result.CommandSequence = &nextSequence
		} else {
			result.CommandSequence = nil
		}
		if replacingExisting {
			// The original result ID is also the outbox message ID and Trade
			// target ID. Allocate a new immutable identity when replacing a
			// historical same-period row, otherwise the old outbox/receipt would
			// make the fresh binding look like a replay.
			suffix := fmt.Sprintf("r%d", nextSequence)
			if result.Action != domain.ActionRebalance {
				suffix = fmt.Sprintf("r%d", result.CreatedAt.UTC().UnixNano())
			}
			result.ID = result.ID + "-" + suffix
		}
		if err := insertResult(tx, result); err != nil {
			return err
		}
		currentTargets := runner.CurrentTargetsJSON
		if result.Action == domain.ActionRebalance {
			currentTargets = string(result.TargetsJSON)
		}
		if err := tx.Exec(`
			UPDATE t_strategy_runners
			SET current_targets_json = ?, command_sequence = ?, last_result_id = ?,
			    last_success_at = ?, last_error = NULL, updated_at = ?
			WHERE runner_id = ?
		`, currentTargets, nextSequence, result.ID, result.CreatedAt.UTC().UnixMilli(), result.CreatedAt.UTC().UnixMilli(), result.RunnerID).Error; err != nil {
			return err
		}
		if result.Action == domain.ActionRebalance && runner.LogicalAccountID.Valid {
			eventData, eventErr := marshalTargetEvent(result, runner)
			if eventErr != nil {
				return eventErr
			}
			if err := tx.Exec(`INSERT INTO t_strategy_outbox (message_id, event_data, created_at) VALUES (?, ?, ?)`, result.ID, eventData, result.CreatedAt.UTC().UnixMilli()).Error; err != nil {
				return err
			}
		}
		outcome = CommitEvaluationOutcome{Result: result, Created: true}
		return nil
	})
	return outcome, err
}

func (s *Store) RecordRunnerFailure(ctx context.Context, runnerID string, failure error, at time.Time) error {
	if strings.TrimSpace(runnerID) == "" || failure == nil {
		return errors.New("runner id and failure are required")
	}
	result := s.db.WithContext(ctx).Exec(`UPDATE t_strategy_runners SET last_error = ?, updated_at = ? WHERE runner_id = ?`, failure.Error(), at.UTC().UnixMilli(), runnerID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// InvalidateEvaluation clears a result committed under an owner generation
// that changed before the corresponding Trade event could be accepted. The
// event is removed when it is still in the outbox; if it was already
// published, Trade's generation fence rejects it. Clearing the live snapshot
// ensures the next ready period is emitted as a rebalance rather than a hold.
func (s *Store) InvalidateEvaluation(ctx context.Context, runnerID, resultID string, at time.Time) error {
	if strings.TrimSpace(runnerID) == "" || strings.TrimSpace(resultID) == "" {
		return errors.New("runner id and result id are required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE t_strategy_runners
			SET current_targets_json = '[]', last_result_id = NULL,
				last_success_at = NULL, last_error = ?, updated_at = ?
			WHERE runner_id = ? AND last_result_id = ?
		`, "owner generation changed during evaluation", at.UTC().UnixMilli(), runnerID, resultID).Error; err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM t_strategy_outbox WHERE message_id = ?`, resultID).Error
	})
}

func (s *Store) GetResult(ctx context.Context, resultID string) (domain.StrategyResult, error) {
	var row resultRow
	err := s.db.WithContext(ctx).Table("t_strategy_results").Where("result_id = ?", resultID).Take(&row).Error
	return row.domain(), err
}

type ResultFilter struct{ RunnerID string }

func (s *Store) ListResults(ctx context.Context, filter ResultFilter) ([]domain.StrategyResult, error) {
	query := s.db.WithContext(ctx).Table("t_strategy_results")
	if filter.RunnerID != "" {
		query = query.Where("runner_id = ?", filter.RunnerID)
	}
	var rows []resultRow
	if err := query.Order("period_time DESC, result_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	results := make([]domain.StrategyResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, row.domain())
	}
	return results, nil
}

func validateCommitEvaluationRequest(request CommitEvaluationRequest) error {
	result := request.Result
	if strings.TrimSpace(result.ID) == "" || strings.TrimSpace(result.RunnerID) == "" || strings.TrimSpace(result.StrategyID) == "" || strings.TrimSpace(result.InputHash) == "" || result.PeriodTime.IsZero() || result.CreatedAt.IsZero() {
		return errors.New("strategy evaluation identity is incomplete")
	}
	if result.Action != request.Evaluation.Action || (result.Action != domain.ActionHold && result.Action != domain.ActionRebalance) {
		return errors.New("strategy evaluation action is invalid or inconsistent")
	}
	return nil
}

func resultOwnerGeneration(result resultRow) int64 {
	if strings.TrimSpace(result.DebugInfoJSON) == "" {
		return 0
	}
	var debug struct {
		OwnerGeneration int64 `json:"owner_generation"`
	}
	if err := json.Unmarshal([]byte(result.DebugInfoJSON), &debug); err != nil {
		return 0
	}
	return debug.OwnerGeneration
}

func insertResult(db *gorm.DB, result domain.StrategyResult) error {
	return db.Exec(`
		INSERT INTO t_strategy_results (
			result_id, runner_id, strategy_id, period_time, targets_json, debug_info_json,
			input_hash, action, command_sequence, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, result.ID, result.RunnerID, result.StrategyID, result.PeriodTime.UTC().UnixMilli(), string(result.TargetsJSON), string(result.DebugInfoJSON), result.InputHash, result.Action, int64Value(result.CommandSequence), result.CreatedAt.UTC().UnixMilli()).Error
}

func encodeEvaluation(evaluation domain.Evaluation) (json.RawMessage, json.RawMessage, error) {
	targets := append([]domain.InstrumentTarget(nil), evaluation.Targets...)
	sort.Slice(targets, func(i, j int) bool { return targets[i].InstrumentID < targets[j].InstrumentID })
	if targets == nil {
		targets = []domain.InstrumentTarget{}
	}
	targetJSON, err := json.Marshal(targets)
	if err != nil {
		return nil, nil, fmt.Errorf("encode strategy targets: %w", err)
	}
	debugJSON, err := json.Marshal(evaluation.DebugInfo)
	if err != nil {
		return nil, nil, fmt.Errorf("encode strategy debug info: %w", err)
	}
	return targetJSON, debugJSON, nil
}

func marshalTargetEvent(result domain.StrategyResult, runner runnerRow) ([]byte, error) {
	var targets []domain.InstrumentTarget
	if err := json.Unmarshal(result.TargetsJSON, &targets); err != nil {
		return nil, fmt.Errorf("decode strategy targets: %w", err)
	}
	payloadTargets := make([]*tradeeventpb.InstrumentWeightTarget, 0, len(targets))
	for _, target := range targets {
		payloadTargets = append(payloadTargets, &tradeeventpb.InstrumentWeightTarget{InstrumentId: target.InstrumentID, TargetWeight: target.TargetWeight})
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	var debug map[string]any
	if len(result.DebugInfoJSON) > 0 {
		if err := json.Unmarshal(result.DebugInfoJSON, &debug); err != nil {
			return nil, fmt.Errorf("decode strategy debug info: %w", err)
		}
	}
	var ownerGeneration int64
	if value, ok := debug["owner_generation"]; ok {
		switch number := value.(type) {
		case float64:
			ownerGeneration = int64(number)
		case json.Number:
			ownerGeneration, _ = number.Int64()
		}
	}
	payload := &tradeeventpb.LogicalAccountTargetWeightRequested{TargetId: result.ID, RunnerId: result.RunnerID, LogicalAccountId: runner.LogicalAccountID.String, CommandSequence: *result.CommandSequence, SignalTime: result.PeriodTime.UTC().Format(time.RFC3339Nano), Targets: payloadTargets, OwnerGeneration: ownerGeneration}
	return registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, payload, events.PublishOptions{EventID: result.ID, OccurredAt: result.CreatedAt.UTC(), SpaceID: runner.SpaceID, SubjectID: runner.LogicalAccountID.String})
}

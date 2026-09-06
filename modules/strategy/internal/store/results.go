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

// The legacy request/result names remain as adapters while callers migrate to
// CommitResult and the instance-scoped result model. They do not reintroduce
// the old database columns.
type CommitEvaluationRequest struct {
	Result          domain.StrategyResult
	Evaluation      domain.Evaluation
	OwnerGeneration int64
}

type CommitEvaluationOutcome struct {
	Result  domain.StrategyResult
	Created bool
}

var ErrLogicalResultConflict = errors.New("strategy result logical key is invalid")
var ErrRunnerNotEnabled = errors.New("strategy instance must be enabled to accept an evaluation")

func (s *Store) CommitEvaluation(ctx context.Context, request CommitEvaluationRequest) (CommitEvaluationOutcome, error) {
	legacy := request.Result
	if strings.TrimSpace(legacy.RunnerID) == "" || strings.TrimSpace(legacy.StrategyID) == "" {
		return CommitEvaluationOutcome{}, errors.New("strategy evaluation identity is incomplete")
	}
	instance, err := s.GetInstance(ctx, legacy.RunnerID)
	if err != nil {
		return CommitEvaluationOutcome{}, err
	}
	if !instance.Enabled || instance.SessionID == nil {
		return CommitEvaluationOutcome{}, ErrRunnerNotEnabled
	}
	targets, ruleStates, err := encodeLegacyEvaluation(request.Evaluation)
	if err != nil {
		return CommitEvaluationOutcome{}, err
	}
	snapshot := json.RawMessage(fmt.Sprintf(`{"strategy_id":%q,"dsl_yaml":null,"inputs":%s}`, legacy.StrategyID, instance.InputBindingsJSON))
	status := PublishNone
	var eventData []byte
	result := StrategyResult{
		ResultID: legacy.ID, InstanceID: legacy.RunnerID, SessionID: *instance.SessionID,
		BarEndTime: legacy.PeriodTime.UTC(), ValidUntil: time.Now().UTC().Add(24 * time.Hour),
		SnapshotJSON: snapshot, TargetsJSON: targets, RuleStatesJSON: ruleStates,
		EventData: eventData, PublishStatus: status, CreatedAt: legacy.CreatedAt.UTC(),
	}
	if instance.LogicalAccountID != nil && request.Evaluation.Action == domain.ActionRebalance {
		status = PublishPending
		eventData, err = marshalLegacyTargetEvent(instance, result, legacy.StrategyID, request.OwnerGeneration)
		if err != nil {
			return CommitEvaluationOutcome{}, err
		}
	}
	result.EventData = eventData
	result.PublishStatus = status
	committed, created, err := s.CommitResult(ctx, CommitResultRequest{Result: result, Now: time.Now().UTC()})
	if err != nil {
		return CommitEvaluationOutcome{}, err
	}
	return CommitEvaluationOutcome{Result: legacyResult(committed, request.Result), Created: created}, nil
}

func (s *Store) RecordRunnerFailure(ctx context.Context, runnerID string, failure error, at time.Time) error {
	if strings.TrimSpace(runnerID) == "" || failure == nil {
		return errors.New("instance id and failure are required")
	}
	// Failures are deliberately not represented as StrategyResult rows. The
	// strategy process owns diagnostics; this adapter only verifies the instance
	// still exists so old callers retain a useful error boundary.
	_, err := s.GetInstance(ctx, runnerID)
	return err
}

func (s *Store) InvalidateEvaluation(ctx context.Context, runnerID, resultID string, at time.Time) error {
	if strings.TrimSpace(runnerID) == "" || strings.TrimSpace(resultID) == "" {
		return errors.New("instance id and result id are required")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE t_strategy_results SET publish_status = 'cancelled'
			WHERE result_id = ? AND instance_id = ? AND publish_status = 'pending'
		`, resultID, runnerID)
		return result.Error
	})
}

func (s *Store) GetResult(ctx context.Context, resultID string) (domain.StrategyResult, error) {
	result, err := s.GetStrategyResult(ctx, resultID)
	if err != nil {
		return domain.StrategyResult{}, err
	}
	return legacyResult(result, domain.StrategyResult{ID: result.ResultID, RunnerID: result.InstanceID, PeriodTime: result.BarEndTime, CreatedAt: result.CreatedAt}), nil
}

type ResultFilter struct{ RunnerID string }

func (s *Store) ListResults(ctx context.Context, filter ResultFilter) ([]domain.StrategyResult, error) {
	query := s.db.WithContext(ctx).Table("t_strategy_results")
	if filter.RunnerID != "" {
		query = query.Where("instance_id = ?", filter.RunnerID)
	}
	var rows []resultRecord
	if err := query.Order("bar_end_time DESC, result_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	results := make([]domain.StrategyResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, legacyResult(row.result(), domain.StrategyResult{}))
	}
	return results, nil
}

func encodeLegacyEvaluation(evaluation domain.Evaluation) (json.RawMessage, json.RawMessage, error) {
	targets := evaluation.Targets
	if targets == nil {
		targets = []domain.InstrumentTarget{}
	}
	targetJSON, err := json.Marshal(targets)
	if err != nil {
		return nil, nil, fmt.Errorf("encode strategy targets: %w", err)
	}
	stateJSON, err := json.Marshal(evaluation.RuleStates)
	if err != nil {
		return nil, nil, fmt.Errorf("encode strategy rule states: %w", err)
	}
	if string(stateJSON) == "null" {
		stateJSON = json.RawMessage(`{}`)
	}
	return targetJSON, stateJSON, nil
}

func marshalLegacyTargetEvent(instance StrategyInstance, result StrategyResult, strategyID string, ownerGeneration int64) ([]byte, error) {
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
	payload := &tradeeventpb.LogicalAccountTargetWeightRequested{
		// CommitEvaluation is the compatibility adapter for the old Runner
		// contract. Keep its identity fields legacy-shaped while preserving the
		// optional owner-generation fence for modern callers.
		TargetId: result.ResultID, RunnerId: result.InstanceID,
		LogicalAccountId: valueOrEmpty(instance.LogicalAccountID), CommandSequence: legacySequence(result.BarEndTime),
		Targets: payloadTargets, OwnerGeneration: ownerGeneration,
	}
	return registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, payload, events.PublishOptions{
		EventID: result.ResultID, OccurredAt: result.CreatedAt.UTC(), SpaceID: instance.SpaceID, SubjectID: valueOrEmpty(instance.LogicalAccountID),
	})
}

func legacyResult(result StrategyResult, seed domain.StrategyResult) domain.StrategyResult {
	action := seed.Action
	if action == "" {
		action = domain.ActionRebalance
	}
	sequence := legacySequence(result.BarEndTime)
	return domain.StrategyResult{
		ID: result.ResultID, RunnerID: result.InstanceID, StrategyID: seed.StrategyID,
		PeriodTime: result.BarEndTime, TargetsJSON: append(json.RawMessage(nil), result.TargetsJSON...),
		DebugInfoJSON: append(json.RawMessage(nil), result.RuleStatesJSON...), InputHash: seed.InputHash,
		Action: action, CommandSequence: &sequence, CreatedAt: result.CreatedAt,
	}
}

func legacySequence(barEnd time.Time) int64 {
	if barEnd.IsZero() || barEnd.UnixMilli() <= 0 {
		return 1
	}
	return barEnd.UnixMilli()
}

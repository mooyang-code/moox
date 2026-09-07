package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

// Read adapters preserve the legacy result shape for historical callers while
// CommitResult remains the only write path for the instance-scoped model.
var ErrLogicalResultConflict = errors.New("strategy result logical key is invalid")

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

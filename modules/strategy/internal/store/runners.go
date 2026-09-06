package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"gorm.io/gorm"
)

var ErrRunnerEnabled = errors.New("strategy instance must be disabled")

// CreateRunner is retained as a source-compatible adapter for callers that
// still use the old source filename. Persistence is always in the instance
// table; source view and frequency are input binding data.
func (s *Store) CreateRunner(ctx context.Context, runner domain.StrategyRunner) error {
	bindings, err := legacyBindings(runner.SourceViewID, runner.Frequency)
	if err != nil {
		return err
	}
	sessionID := (*string)(nil)
	if runner.Status == domain.RunnerStatusEnabled {
		value, err := newSessionID()
		if err != nil {
			return err
		}
		sessionID = &value
	}
	return s.CreateInstance(ctx, StrategyInstance{
		InstanceID: runner.ID, StrategyID: runner.StrategyID, SpaceID: runner.SpaceID,
		InputBindingsJSON: bindings, LogicalAccountID: runner.LogicalAccountID,
		Enabled: runner.Status == domain.RunnerStatusEnabled, SessionID: sessionID,
		CreatedAt: runner.CreatedAt, UpdatedAt: runner.UpdatedAt,
	})
}

func (s *Store) UpdateRunner(ctx context.Context, runner domain.StrategyRunner) error {
	bindings, err := legacyBindings(runner.SourceViewID, runner.Frequency)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current instanceRow
		if err := tx.Table("t_strategy_instances").Where("instance_id = ?", runner.ID).Take(&current).Error; err != nil {
			return err
		}
		if current.Enabled == 1 {
			return ErrRunnerEnabled
		}
		result := tx.Exec(`
			UPDATE t_strategy_instances
			SET strategy_id = ?, space_id = ?, input_bindings_json = ?, logical_account_id = ?, updated_at = ?
			WHERE instance_id = ? AND enabled = 0
		`, runner.StrategyID, runner.SpaceID, string(bindings), stringValue(runner.LogicalAccountID), runner.UpdatedAt.UTC().UnixMilli(), runner.ID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return runnerMutationError(ctx, tx, runner.ID)
		}
		return nil
	})
}

func (s *Store) SetRunnerStatus(ctx context.Context, runnerID string, status domain.RunnerStatus, updatedAt time.Time) error {
	if status != domain.RunnerStatusDisabled && status != domain.RunnerStatusEnabled {
		return errors.New("invalid strategy runner status")
	}
	if strings.TrimSpace(runnerID) == "" || updatedAt.IsZero() {
		return errors.New("runner id and update time are required")
	}
	current, err := s.GetInstance(ctx, runnerID)
	if err != nil {
		return err
	}
	if status == domain.RunnerStatusEnabled && current.Enabled {
		return nil
	}
	var sessionID *string
	if status == domain.RunnerStatusEnabled {
		value, err := newSessionID()
		if err != nil {
			return err
		}
		sessionID = &value
	}
	return s.SetInstanceEnabled(ctx, runnerID, status == domain.RunnerStatusEnabled, sessionID, updatedAt)
}

// ResetRunnerLifecycle is obsolete with session-scoped results. It clears no
// result rows; a subsequent explicit enable creates a fresh session.
func (s *Store) ResetRunnerLifecycle(ctx context.Context, runnerID string, expectedGeneration int64, at time.Time) error {
	if strings.TrimSpace(runnerID) == "" {
		return errors.New("runner id is required")
	}
	if _, err := s.GetInstance(ctx, runnerID); err != nil {
		return err
	}
	// Results are immutable audit records and remain valid across sessions. Move
	// the legacy adapter to a fresh session instead of deleting rows; this makes
	// the current-target projection empty while preserving the audit trail.
	sessionID, err := newSessionID()
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Exec("UPDATE t_strategy_instances SET session_id = ?, updated_at = ? WHERE instance_id = ?", sessionID, at.UTC().UnixMilli(), runnerID).Error
}

func runnerMutationError(ctx context.Context, db *gorm.DB, runnerID string) error {
	var count int64
	if err := db.WithContext(ctx).Table("t_strategy_instances").Where("instance_id = ?", runnerID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return ErrRunnerEnabled
}

func (s *Store) GetRunner(ctx context.Context, runnerID string) (domain.StrategyRunner, error) {
	instance, err := s.GetInstance(ctx, runnerID)
	if err != nil {
		return domain.StrategyRunner{}, err
	}
	var binding struct {
		SourceViewID string `json:"source_view_id"`
		Frequency    string `json:"frequency"`
	}
	_ = json.Unmarshal(instance.InputBindingsJSON, &binding)
	status := domain.RunnerStatusDisabled
	if instance.Enabled {
		status = domain.RunnerStatusEnabled
	}
	runner := domain.StrategyRunner{
		ID: instance.InstanceID, StrategyID: instance.StrategyID, SpaceID: instance.SpaceID,
		SourceViewID: binding.SourceViewID, Frequency: binding.Frequency,
		LogicalAccountID: instance.LogicalAccountID, Status: status,
		CurrentTargetsJSON: json.RawMessage(`[]`), CreatedAt: instance.CreatedAt, UpdatedAt: instance.UpdatedAt,
	}
	latest, err := s.LatestResult(ctx, instance.InstanceID, valueOrEmpty(instance.SessionID))
	if err == nil {
		runner.CurrentTargetsJSON = append(json.RawMessage(nil), latest.TargetsJSON...)
	}
	return runner, nil
}

func legacyBindings(sourceViewID, frequency string) (json.RawMessage, error) {
	value, err := json.Marshal(struct {
		SourceViewID string `json:"source_view_id"`
		Frequency    string `json:"frequency"`
	}{sourceViewID, frequency})
	if err != nil {
		return nil, err
	}
	return value, nil
}

func newSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

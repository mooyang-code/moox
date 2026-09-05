package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

// StrategyDefinition is the persisted form of a shared strategy DSL. The
// strategy name is derived from the DSL by the registry before it is saved.
// It is intentionally kept independent from an execution instance.
type StrategyDefinition struct {
	StrategyID   string
	StrategyName string
	DSLYaml      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// StrategyDef is the short name used by the design documents.
type StrategyDef = StrategyDefinition

// StrategyInstance binds a StrategyDefinition to one space, optional account
// and execution session. A nil account is an observation-only instance.
type StrategyInstance struct {
	InstanceID        string
	StrategyID        string
	SpaceID           string
	InputBindingsJSON json.RawMessage
	LogicalAccountID  *string
	Enabled           bool
	SessionID         *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type instanceRow struct {
	InstanceID        string         `gorm:"column:instance_id"`
	StrategyID        string         `gorm:"column:strategy_id"`
	SpaceID           string         `gorm:"column:space_id"`
	InputBindingsJSON string         `gorm:"column:input_bindings_json"`
	LogicalAccountID  sql.NullString `gorm:"column:logical_account_id"`
	Enabled           int            `gorm:"column:enabled"`
	SessionID         sql.NullString `gorm:"column:session_id"`
	CreatedAt         int64          `gorm:"column:created_at"`
	UpdatedAt         int64          `gorm:"column:updated_at"`
}

func (r instanceRow) instance() StrategyInstance {
	return StrategyInstance{
		InstanceID: r.InstanceID, StrategyID: r.StrategyID, SpaceID: r.SpaceID,
		InputBindingsJSON: json.RawMessage(r.InputBindingsJSON),
		LogicalAccountID:  nullableString(r.LogicalAccountID), Enabled: r.Enabled == 1,
		SessionID: nullableString(r.SessionID), CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
		UpdatedAt: time.UnixMilli(r.UpdatedAt).UTC(),
	}
}

// PublishStatus describes the durable publication state held with a result.
type PublishStatus string

const (
	PublishNone      PublishStatus = "none"
	PublishPending   PublishStatus = "pending"
	PublishSent      PublishStatus = "sent"
	PublishCancelled PublishStatus = "cancelled"
)

// StrategyResult is one successful calculation and its immutable publication
// snapshot. Only PublishStatus may change after insertion.
type StrategyResult struct {
	ResultID       string
	InstanceID     string
	SessionID      string
	BarEndTime     time.Time
	ValidUntil     time.Time
	SnapshotJSON   json.RawMessage
	TargetsJSON    json.RawMessage
	RuleStatesJSON json.RawMessage
	EventData      []byte
	PublishStatus  PublishStatus
	CreatedAt      time.Time
}

type resultRecord struct {
	ResultID       string        `gorm:"column:result_id"`
	InstanceID     string        `gorm:"column:instance_id"`
	SessionID      string        `gorm:"column:session_id"`
	BarEndTime     int64         `gorm:"column:bar_end_time"`
	ValidUntil     int64         `gorm:"column:valid_until"`
	SnapshotJSON   string        `gorm:"column:snapshot_json"`
	TargetsJSON    string        `gorm:"column:targets_json"`
	RuleStatesJSON string        `gorm:"column:rule_states_json"`
	EventData      []byte        `gorm:"column:event_data"`
	PublishStatus  PublishStatus `gorm:"column:publish_status"`
	CreatedAt      int64         `gorm:"column:created_at"`
}

func (r resultRecord) result() StrategyResult {
	return StrategyResult{
		ResultID: r.ResultID, InstanceID: r.InstanceID, SessionID: r.SessionID,
		BarEndTime: time.UnixMilli(r.BarEndTime).UTC(), ValidUntil: time.UnixMilli(r.ValidUntil).UTC(),
		SnapshotJSON: json.RawMessage(r.SnapshotJSON), TargetsJSON: json.RawMessage(r.TargetsJSON),
		RuleStatesJSON: json.RawMessage(r.RuleStatesJSON), EventData: append([]byte(nil), r.EventData...),
		PublishStatus: r.PublishStatus, CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
	}
}

func (s *Store) SaveStrategyDefinition(ctx context.Context, def StrategyDefinition) error {
	if strings.TrimSpace(def.StrategyID) == "" || strings.TrimSpace(def.StrategyName) == "" || strings.TrimSpace(def.DSLYaml) == "" || def.CreatedAt.IsZero() || def.UpdatedAt.IsZero() {
		return errors.New("strategy definition identity is incomplete")
	}
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategies (strategy_id, strategy_name, dsl_yaml, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, def.StrategyID, def.StrategyName, def.DSLYaml, def.CreatedAt.UTC().UnixMilli(), def.UpdatedAt.UTC().UnixMilli()).Error
}

// SaveStrategyDef is an explicit alias matching the domain vocabulary.
func (s *Store) SaveStrategyDef(ctx context.Context, def StrategyDef) error {
	return s.SaveStrategyDefinition(ctx, def)
}

func (s *Store) GetStrategyDefinition(ctx context.Context, strategyID string) (StrategyDefinition, error) {
	var row struct {
		StrategyID   string `gorm:"column:strategy_id"`
		StrategyName string `gorm:"column:strategy_name"`
		DSLYaml      string `gorm:"column:dsl_yaml"`
		CreatedAt    int64  `gorm:"column:created_at"`
		UpdatedAt    int64  `gorm:"column:updated_at"`
	}
	err := s.db.WithContext(ctx).Table("t_strategies").Where("strategy_id = ?", strategyID).Take(&row).Error
	if err != nil {
		return StrategyDefinition{}, err
	}
	return StrategyDefinition{
		StrategyID: row.StrategyID, StrategyName: row.StrategyName, DSLYaml: row.DSLYaml,
		CreatedAt: time.UnixMilli(row.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(row.UpdatedAt).UTC(),
	}, nil
}

// UpdateStrategyDefinition changes a shared DSL only when every referencing
// instance is disabled. The caller is responsible for parsing the DSL and
// deriving StrategyName before entering this transaction.
func (s *Store) UpdateStrategyDefinition(ctx context.Context, def StrategyDefinition) error {
	if strings.TrimSpace(def.StrategyID) == "" || strings.TrimSpace(def.StrategyName) == "" || strings.TrimSpace(def.DSLYaml) == "" || def.UpdatedAt.IsZero() {
		return errors.New("strategy definition identity is incomplete")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Table("t_strategy_instances").Where("strategy_id = ? AND (enabled = 1 OR session_id IS NOT NULL)", def.StrategyID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return errors.New("strategy definition is referenced by an enabled instance")
		}
		result := tx.Exec(`
			UPDATE t_strategies SET strategy_name = ?, dsl_yaml = ?, updated_at = ? WHERE strategy_id = ?
		`, def.StrategyName, def.DSLYaml, def.UpdatedAt.UTC().UnixMilli(), def.StrategyID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (s *Store) ListStrategyDefinitions(ctx context.Context, name string) ([]StrategyDefinition, error) {
	query := s.db.WithContext(ctx).Table("t_strategies")
	if strings.TrimSpace(name) != "" {
		query = query.Where("strategy_name LIKE ?", "%"+name+"%")
	}
	var rows []struct {
		StrategyID   string `gorm:"column:strategy_id"`
		StrategyName string `gorm:"column:strategy_name"`
		DSLYaml      string `gorm:"column:dsl_yaml"`
		CreatedAt    int64  `gorm:"column:created_at"`
		UpdatedAt    int64  `gorm:"column:updated_at"`
	}
	if err := query.Order("strategy_name, strategy_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	defs := make([]StrategyDefinition, 0, len(rows))
	for _, row := range rows {
		defs = append(defs, StrategyDefinition{
			StrategyID: row.StrategyID, StrategyName: row.StrategyName, DSLYaml: row.DSLYaml,
			CreatedAt: time.UnixMilli(row.CreatedAt).UTC(), UpdatedAt: time.UnixMilli(row.UpdatedAt).UTC(),
		})
	}
	return defs, nil
}

func (s *Store) CreateInstance(ctx context.Context, instance StrategyInstance) error {
	if err := validateInstance(instance); err != nil {
		return err
	}
	input := instance.InputBindingsJSON
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if !json.Valid(input) || string(input) == "null" {
		return errors.New("strategy instance input_bindings_json must be valid JSON")
	}
	return s.db.WithContext(ctx).Exec(`
		INSERT INTO t_strategy_instances (
			instance_id, strategy_id, space_id, input_bindings_json, logical_account_id,
			enabled, session_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, instance.InstanceID, instance.StrategyID, instance.SpaceID, string(input),
		stringValue(instance.LogicalAccountID), boolInt(instance.Enabled), stringValue(instance.SessionID),
		instance.CreatedAt.UTC().UnixMilli(), instance.UpdatedAt.UTC().UnixMilli()).Error
}

func (s *Store) GetInstance(ctx context.Context, instanceID string) (StrategyInstance, error) {
	var row instanceRow
	err := s.db.WithContext(ctx).Table("t_strategy_instances").Where("instance_id = ?", instanceID).Take(&row).Error
	if err != nil {
		return StrategyInstance{}, err
	}
	return row.instance(), nil
}

// UpdateInstance updates the definition and input binding of a disabled
// instance. Session and enabled state are controlled by SetInstanceEnabled.
func (s *Store) UpdateInstance(ctx context.Context, instance StrategyInstance) error {
	if err := validateInstance(instance); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current instanceRow
		if err := tx.Table("t_strategy_instances").Where("instance_id = ?", instance.InstanceID).Take(&current).Error; err != nil {
			return err
		}
		if current.Enabled == 1 {
			return errors.New("strategy instance must be disabled")
		}
		if current.SessionID.Valid {
			return errors.New("strategy instance control operation is unfinished")
		}
		input := instance.InputBindingsJSON
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		if !json.Valid(input) || string(input) == "null" {
			return errors.New("strategy instance input_bindings_json must be valid JSON")
		}
		return tx.Exec(`
			UPDATE t_strategy_instances
			SET strategy_id = ?, space_id = ?, input_bindings_json = ?, logical_account_id = ?, updated_at = ?
			WHERE instance_id = ? AND enabled = 0
		`, instance.StrategyID, instance.SpaceID, string(input), stringValue(instance.LogicalAccountID),
			instance.UpdatedAt.UTC().UnixMilli(), instance.InstanceID).Error
	})
}

// SetInstanceEnabled persists the lifecycle state. Enabling requires a new
// non-empty session ID. Disabling with a session ID retains that identity until
// the control plane confirms Trade release; disabling without one clears an
// observation-only session immediately.
func (s *Store) SetInstanceEnabled(ctx context.Context, instanceID string, enabled bool, sessionID *string, at time.Time) error {
	if strings.TrimSpace(instanceID) == "" || at.IsZero() {
		return errors.New("instance id and update time are required")
	}
	if enabled && (sessionID == nil || strings.TrimSpace(*sessionID) == "") {
		return errors.New("enabled strategy instance requires session id")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current instanceRow
		if err := tx.Table("t_strategy_instances").Where("instance_id = ?", instanceID).Take(&current).Error; err != nil {
			return err
		}
		if enabled {
			if current.Enabled == 1 {
				if current.SessionID.Valid && current.SessionID.String == *sessionID {
					return nil
				}
				return errors.New("strategy instance is already enabled")
			}
			if current.SessionID.Valid && current.SessionID.String != *sessionID {
				return errors.New("strategy instance control operation is unfinished")
			}
			return tx.Exec(`UPDATE t_strategy_instances SET enabled = 1, session_id = ?, updated_at = ? WHERE instance_id = ?`, *sessionID, at.UTC().UnixMilli(), instanceID).Error
		}
		if sessionID != nil && strings.TrimSpace(*sessionID) != "" {
			if current.SessionID.Valid && current.SessionID.String != strings.TrimSpace(*sessionID) {
				return errors.New("strategy instance control operation is unfinished")
			}
			return tx.Exec(`UPDATE t_strategy_instances SET enabled = 0, session_id = ?, updated_at = ? WHERE instance_id = ?`, *sessionID, at.UTC().UnixMilli(), instanceID).Error
		}
		// Observation-only instances have no Trade owner to release. Clearing
		// their session in the same transaction prevents a later enable from
		// being blocked by a stale control-operation marker.
		return tx.Exec(`UPDATE t_strategy_instances SET enabled = 0, session_id = NULL, updated_at = ? WHERE instance_id = ?`, at.UTC().UnixMilli(), instanceID).Error
	})
}

// ClearInstanceSession completes a previously persisted disable only after
// the caller has confirmed that Trade released the matching session. It is a
// compare-and-swap so a delayed release cannot clear a newer session.
func (s *Store) ClearInstanceSession(ctx context.Context, instanceID, expectedSessionID string, at time.Time) error {
	if strings.TrimSpace(instanceID) == "" || strings.TrimSpace(expectedSessionID) == "" || at.IsZero() {
		return errors.New("instance id, expected session and update time are required")
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_strategy_instances SET session_id = NULL, updated_at = ?
		WHERE instance_id = ? AND enabled = 0 AND session_id = ?
	`, at.UTC().UnixMilli(), instanceID, expectedSessionID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) ListInstances(ctx context.Context, spaceID string, enabled *bool) ([]StrategyInstance, error) {
	query := s.db.WithContext(ctx).Table("t_strategy_instances")
	if strings.TrimSpace(spaceID) != "" {
		query = query.Where("space_id = ?", spaceID)
	}
	if enabled != nil {
		query = query.Where("enabled = ?", boolInt(*enabled))
	}
	var rows []instanceRow
	if err := query.Order("created_at DESC, instance_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	instances := make([]StrategyInstance, 0, len(rows))
	for _, row := range rows {
		instances = append(instances, row.instance())
	}
	return instances, nil
}

// ListAllInstances is used by the process-wide scheduler, which must refresh
// jobs across every space while the RPC API remains space-scoped.
func (s *Store) ListAllInstances(ctx context.Context, enabled *bool) ([]StrategyInstance, error) {
	return s.ListInstances(ctx, "", enabled)
}

type CommitResultRequest struct {
	Result           StrategyResult
	ExpectedResultID *string
	Now              time.Time
}

// CommitResult atomically inserts one successful result. The expected result
// ID is intentionally an in-memory compare-and-swap value and is never stored.
func (s *Store) CommitResult(ctx context.Context, request CommitResultRequest) (StrategyResult, bool, error) {
	if err := validateResult(request.Result); err != nil {
		return StrategyResult{}, false, err
	}
	if request.Now.IsZero() {
		request.Now = time.Now().UTC()
	}
	var committed StrategyResult
	created := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var instance instanceRow
		if err := tx.Table("t_strategy_instances").Where("instance_id = ?", request.Result.InstanceID).Take(&instance).Error; err != nil {
			return err
		}
		if instance.Enabled != 1 || !instance.SessionID.Valid || instance.SessionID.String != request.Result.SessionID {
			return errors.New("strategy instance is not enabled for this session")
		}
		if !request.Result.ValidUntil.After(request.Now.UTC()) {
			return errors.New("strategy result is expired")
		}
		latest, latestErr := latestResult(tx, request.Result.InstanceID, request.Result.SessionID)
		if latestErr != nil && !errors.Is(latestErr, gorm.ErrRecordNotFound) {
			return latestErr
		}
		if latestErr == nil {
			if request.Result.BarEndTime.Before(latest.BarEndTime) {
				return errors.New("strategy result is older than the latest committed bar")
			}
			if request.ExpectedResultID != nil && latest.ResultID != *request.ExpectedResultID {
				return errors.New("strategy result compare-and-swap conflict")
			}
		} else if request.ExpectedResultID != nil && *request.ExpectedResultID != "" {
			return errors.New("strategy result compare-and-swap conflict")
		}
		var existing resultRecord
		err := tx.Table("t_strategy_results").Where(
			"instance_id = ? AND session_id = ? AND bar_end_time = ?",
			request.Result.InstanceID, request.Result.SessionID, request.Result.BarEndTime.UTC().UnixMilli(),
		).Take(&existing).Error
		if err == nil {
			committed = existing.result()
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := insertResultRecord(tx, request.Result); err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE t_strategy_results
			SET publish_status = 'cancelled'
			WHERE instance_id = ? AND session_id = ? AND bar_end_time < ? AND publish_status = 'pending'
		`, request.Result.InstanceID, request.Result.SessionID, request.Result.BarEndTime.UTC().UnixMilli()).Error; err != nil {
			return err
		}
		committed = request.Result
		created = true
		return nil
	})
	return committed, created, err
}

func latestResult(tx *gorm.DB, instanceID, sessionID string) (StrategyResult, error) {
	var row resultRecord
	err := tx.Table("t_strategy_results").Where("instance_id = ? AND session_id = ?", instanceID, sessionID).Order("bar_end_time DESC, result_id DESC").Take(&row).Error
	if err != nil {
		return StrategyResult{}, err
	}
	return row.result(), nil
}

func (s *Store) LatestResult(ctx context.Context, instanceID, sessionID string) (StrategyResult, error) {
	return latestResult(s.db.WithContext(ctx), instanceID, sessionID)
}

func (s *Store) GetStrategyResult(ctx context.Context, resultID string) (StrategyResult, error) {
	var row resultRecord
	err := s.db.WithContext(ctx).Table("t_strategy_results").Where("result_id = ?", resultID).Take(&row).Error
	if err != nil {
		return StrategyResult{}, err
	}
	return row.result(), nil
}

func (s *Store) ListStrategyResults(ctx context.Context, instanceID, sessionID string) ([]StrategyResult, error) {
	query := s.db.WithContext(ctx).Table("t_strategy_results").Where("instance_id = ?", instanceID)
	if strings.TrimSpace(sessionID) != "" {
		query = query.Where("session_id = ?", sessionID)
	}
	var rows []resultRecord
	if err := query.Order("bar_end_time DESC, result_id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	results := make([]StrategyResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, row.result())
	}
	return results, nil
}

func (s *Store) ListPendingResults(ctx context.Context) ([]StrategyResult, error) {
	var rows []resultRecord
	if err := s.db.WithContext(ctx).Table("t_strategy_results").Where("publish_status = ?", PublishPending).Order("created_at, result_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	results := make([]StrategyResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, row.result())
	}
	return results, nil
}

// PreparePendingResult rechecks the durable publication preconditions just
// before a broker send. Invalid rows are terminally cancelled and do not block
// later results; temporary broker failures are handled by the publisher and
// leave the row pending.
func (s *Store) PreparePendingResult(ctx context.Context, resultID string, now time.Time) (StrategyResult, bool, error) {
	if strings.TrimSpace(resultID) == "" || now.IsZero() {
		return StrategyResult{}, false, errors.New("result id and current time are required")
	}
	var result StrategyResult
	valid := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row resultRecord
		if err := tx.Table("t_strategy_results").Where("result_id = ? AND publish_status = ?", resultID, PublishPending).Take(&row).Error; err != nil {
			return err
		}
		result = row.result()
		var instance instanceRow
		if err := tx.Table("t_strategy_instances").Where("instance_id = ?", row.InstanceID).Take(&instance).Error; err != nil {
			return err
		}
		valid = instance.Enabled == 1 && instance.SessionID.Valid && instance.SessionID.String == row.SessionID && row.ValidUntil > now.UTC().UnixMilli()
		if valid {
			var latest resultRecord
			if err := tx.Table("t_strategy_results").Where("instance_id = ? AND session_id = ?", row.InstanceID, row.SessionID).Order("bar_end_time DESC, result_id DESC").Take(&latest).Error; err != nil {
				return err
			}
			valid = latest.ResultID == row.ResultID
		}
		if !valid {
			if err := tx.Exec(`UPDATE t_strategy_results SET publish_status = 'cancelled' WHERE result_id = ? AND publish_status = 'pending'`, resultID).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return result, valid, err
}

func (s *Store) TransitionPublishStatus(ctx context.Context, resultID string, from, to PublishStatus) error {
	if from != PublishPending || (to != PublishSent && to != PublishCancelled) {
		return errors.New("invalid strategy publish status transition")
	}
	result := s.db.WithContext(ctx).Exec(`UPDATE t_strategy_results SET publish_status = ? WHERE result_id = ? AND publish_status = ?`, to, resultID, from)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func validateInstance(instance StrategyInstance) error {
	if strings.TrimSpace(instance.InstanceID) == "" || strings.TrimSpace(instance.StrategyID) == "" || strings.TrimSpace(instance.SpaceID) == "" || instance.CreatedAt.IsZero() || instance.UpdatedAt.IsZero() {
		return errors.New("strategy instance identity is incomplete")
	}
	return nil
}

func validateResult(result StrategyResult) error {
	if strings.TrimSpace(result.ResultID) == "" || strings.TrimSpace(result.InstanceID) == "" || strings.TrimSpace(result.SessionID) == "" || result.BarEndTime.IsZero() || result.ValidUntil.IsZero() || result.CreatedAt.IsZero() {
		return errors.New("strategy result identity is incomplete")
	}
	if result.PublishStatus != PublishNone && result.PublishStatus != PublishPending && result.PublishStatus != PublishSent && result.PublishStatus != PublishCancelled {
		return errors.New("invalid strategy publish status")
	}
	if len(result.SnapshotJSON) == 0 || len(result.TargetsJSON) == 0 || len(result.RuleStatesJSON) == 0 {
		return errors.New("strategy result snapshots are required")
	}
	if result.PublishStatus == PublishNone && len(result.EventData) != 0 {
		return errors.New("observation result cannot have event data")
	}
	if result.PublishStatus != PublishNone && len(result.EventData) == 0 {
		return errors.New("published result requires event data")
	}
	return nil
}

func insertResultRecord(tx *gorm.DB, result StrategyResult) error {
	return tx.Exec(`
		INSERT INTO t_strategy_results (
			result_id, instance_id, session_id, bar_end_time, valid_until, snapshot_json,
			targets_json, rule_states_json, event_data, publish_status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, result.ResultID, result.InstanceID, result.SessionID, result.BarEndTime.UTC().UnixMilli(), result.ValidUntil.UTC().UnixMilli(),
		string(result.SnapshotJSON), string(result.TargetsJSON), string(result.RuleStatesJSON), result.EventData, result.PublishStatus, result.CreatedAt.UTC().UnixMilli()).Error
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

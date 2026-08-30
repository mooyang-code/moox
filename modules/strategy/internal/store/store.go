// Package store owns Strategy persistence adapters.
package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"gorm.io/gorm"
)

type Store struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Store { return &Store{db: db} }

type strategyRow struct {
	ID           string `gorm:"column:strategy_id"`
	Name         string `gorm:"column:name"`
	Kind         string `gorm:"column:kind"`
	ManifestYAML string `gorm:"column:manifest_yaml"`
	CompiledJSON string `gorm:"column:compiled_json"`
	SourceHash   string `gorm:"column:source_hash"`
	CreatedAt    int64  `gorm:"column:created_at"`
}

func (r strategyRow) domain() domain.Strategy {
	return domain.Strategy{
		ID: r.ID, Name: r.Name, Kind: r.Kind, ManifestYAML: r.ManifestYAML, CompiledJSON: []byte(r.CompiledJSON),
		SourceHash: r.SourceHash, CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
	}
}

type runnerRow struct {
	ID                 string         `gorm:"column:runner_id"`
	StrategyID         string         `gorm:"column:strategy_id"`
	SpaceID            string         `gorm:"column:space_id"`
	SourceViewID       string         `gorm:"column:source_view_id"`
	Frequency          string         `gorm:"column:frequency"`
	LogicalAccountID   sql.NullString `gorm:"column:logical_account_id"`
	Status             string         `gorm:"column:status"`
	CurrentTargetsJSON string         `gorm:"column:current_targets_json"`
	CommandSequence    int64          `gorm:"column:command_sequence"`
	LastResultID       sql.NullString `gorm:"column:last_result_id"`
	LastSuccessAt      sql.NullInt64  `gorm:"column:last_success_at"`
	LastError          sql.NullString `gorm:"column:last_error"`
	CreatedAt          int64          `gorm:"column:created_at"`
	UpdatedAt          int64          `gorm:"column:updated_at"`
}

func (r runnerRow) domain() domain.StrategyRunner {
	return domain.StrategyRunner{
		ID:                 r.ID,
		StrategyID:         r.StrategyID,
		SpaceID:            r.SpaceID,
		SourceViewID:       r.SourceViewID,
		Frequency:          r.Frequency,
		LogicalAccountID:   nullableString(r.LogicalAccountID),
		Status:             domain.RunnerStatus(r.Status),
		CurrentTargetsJSON: json.RawMessage(r.CurrentTargetsJSON),
		CommandSequence:    r.CommandSequence,
		LastResultID:       nullableString(r.LastResultID),
		LastSuccessAt:      nullableTime(r.LastSuccessAt),
		LastError:          nullableString(r.LastError),
		CreatedAt:          time.UnixMilli(r.CreatedAt).UTC(),
		UpdatedAt:          time.UnixMilli(r.UpdatedAt).UTC(),
	}
}

type resultRow struct {
	ID              string        `gorm:"column:result_id"`
	RunnerID        string        `gorm:"column:runner_id"`
	StrategyID      string        `gorm:"column:strategy_id"`
	PeriodTime      int64         `gorm:"column:period_time"`
	TargetsJSON     string        `gorm:"column:targets_json"`
	DebugInfoJSON   string        `gorm:"column:debug_info_json"`
	InputHash       string        `gorm:"column:input_hash"`
	Action          string        `gorm:"column:action"`
	CommandSequence sql.NullInt64 `gorm:"column:command_sequence"`
	CreatedAt       int64         `gorm:"column:created_at"`
}

func (r resultRow) domain() domain.StrategyResult {
	return domain.StrategyResult{
		ID: r.ID, RunnerID: r.RunnerID, StrategyID: r.StrategyID,
		PeriodTime: time.UnixMilli(r.PeriodTime).UTC(), TargetsJSON: json.RawMessage(r.TargetsJSON), DebugInfoJSON: json.RawMessage(r.DebugInfoJSON),
		InputHash: r.InputHash, Action: domain.Action(r.Action),
		CommandSequence: nullableInt64(r.CommandSequence), CreatedAt: time.UnixMilli(r.CreatedAt).UTC(),
	}
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	result := time.UnixMilli(value.Int64).UTC()
	return &result
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func stringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func timeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().UnixMilli()
}

func int64Value(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

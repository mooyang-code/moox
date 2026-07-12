package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Page struct {
	Number int
	Size   int
}

func (p Page) normalized() Page {
	if p.Number < 1 {
		p.Number = 1
	}
	if p.Size < 1 {
		p.Size = 20
	}
	if p.Size > 200 {
		p.Size = 200
	}
	return p
}

type RunningFilter struct {
	SpaceID, Status, Mode, StrategyID string
}

type RunFilter struct {
	BindingID string
	From      time.Time
	To        time.Time
}

type PerformanceFilter struct {
	BindingID string
	Source    string
	From      time.Time
	To        time.Time
}

func (r *Store) UpsertHealth(ctx context.Context, health domain.BindingHealth) error {
	if r == nil || r.db == nil {
		return errors.New("strategy repository is unavailable")
	}
	if health.BindingID == "" {
		return errors.New("binding_id is required")
	}
	if health.ObservedAt.IsZero() {
		health.ObservedAt = time.Now().UTC()
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_binding_id"}}, DoUpdates: clause.AssignmentColumns([]string{"c_status", "c_mode", "c_last_run_id", "c_last_success_at", "c_last_error_type", "c_last_error_message", "c_last_data_revision", "c_data_cutoff", "c_worker_status", "c_outbox_lag_seconds", "c_observed_at"})}).Create(&health).Error
}

func (r *Store) ListRunningStrategies(ctx context.Context, filter RunningFilter, page Page) ([]domain.RunningStrategySummary, int64, error) {
	if r == nil || r.db == nil {
		return nil, 0, errors.New("strategy repository is unavailable")
	}
	p := page.normalized()
	query := r.db.WithContext(ctx).Table("t_strategy_bindings AS b").
		Select("b.c_strategy_id AS strategy_id, b.c_strategy_version AS version, b.c_binding_id AS binding_id, b.c_space_id AS space_id, b.c_view_id AS view_id, b.c_freq AS freq, COALESCE((SELECT e.c_mode FROM t_strategy_execution_bindings AS e WHERE e.c_group_id=b.c_group_id AND e.c_status='enabled' LIMIT 1), 'observe') AS mode, b.c_status AS status, d.c_source_hash AS source_hash, COALESCE(s.c_last_run_id, '') AS last_run_id, h.c_status AS health_status, h.c_last_success_at AS last_success_at, h.c_last_error_type AS last_error_type, h.c_last_error_message AS last_error_message, h.c_last_data_revision AS last_data_revision, h.c_data_cutoff AS data_cutoff, h.c_worker_status AS worker_status, h.c_outbox_lag_seconds AS outbox_lag_seconds, h.c_observed_at AS observed_at").
		Joins("JOIN t_strategy_defs AS d ON d.c_strategy_id=b.c_strategy_id AND d.c_version=b.c_strategy_version").
		Joins("LEFT JOIN t_strategy_states AS s ON s.c_binding_id=b.c_binding_id").
		Joins("LEFT JOIN t_strategy_binding_health AS h ON h.c_binding_id=b.c_binding_id")
	if filter.SpaceID != "" {
		query = query.Where("b.c_space_id = ?", filter.SpaceID)
	}
	if filter.Status != "" {
		query = query.Where("b.c_status = ?", filter.Status)
	}
	if filter.Mode != "" {
		query = query.Where("COALESCE((SELECT e.c_mode FROM t_strategy_execution_bindings AS e WHERE e.c_group_id=b.c_group_id AND e.c_status='enabled' LIMIT 1), 'observe') = ?", filter.Mode)
	}
	if filter.StrategyID != "" {
		query = query.Where("b.c_strategy_id = ?", filter.StrategyID)
	}
	var total int64
	countQuery := query.Session(&gorm.Session{})
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []runningRow
	if err := query.Order("CASE WHEN h.c_status IN ('failed','unknown') THEN 0 ELSE 1 END, h.c_observed_at DESC, b.c_binding_id").Offset((p.Number - 1) * p.Size).Limit(p.Size).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]domain.RunningStrategySummary, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.summary())
	}
	return items, total, nil
}

type runningRow struct {
	StrategyID       string    `gorm:"column:strategy_id"`
	Version          string    `gorm:"column:version"`
	BindingID        string    `gorm:"column:binding_id"`
	SpaceID          string    `gorm:"column:space_id"`
	ViewID           string    `gorm:"column:view_id"`
	Freq             string    `gorm:"column:freq"`
	Mode             string    `gorm:"column:mode"`
	Status           string    `gorm:"column:status"`
	SourceHash       string    `gorm:"column:source_hash"`
	LastRunID        string    `gorm:"column:last_run_id"`
	HealthStatus     string    `gorm:"column:health_status"`
	LastSuccessAt    time.Time `gorm:"column:last_success_at"`
	LastErrorType    string    `gorm:"column:last_error_type"`
	LastErrorMessage string    `gorm:"column:last_error_message"`
	LastDataRevision string    `gorm:"column:last_data_revision"`
	DataCutoff       time.Time `gorm:"column:data_cutoff"`
	WorkerStatus     string    `gorm:"column:worker_status"`
	OutboxLagSeconds int64     `gorm:"column:outbox_lag_seconds"`
	ObservedAt       time.Time `gorm:"column:observed_at"`
}

func (r runningRow) summary() domain.RunningStrategySummary {
	return domain.RunningStrategySummary{
		StrategyID: r.StrategyID, Version: r.Version, BindingID: r.BindingID, SpaceID: r.SpaceID,
		ViewID: r.ViewID, Freq: r.Freq, Mode: r.Mode, Status: r.Status, SourceHash: r.SourceHash,
		LastRunID: r.LastRunID, LastDataRevision: r.LastDataRevision,
		Health: domain.BindingHealth{BindingID: r.BindingID, Status: r.HealthStatus, Mode: r.Mode, LastRunID: r.LastRunID, LastSuccessAt: r.LastSuccessAt, LastErrorType: r.LastErrorType, LastErrorMessage: r.LastErrorMessage, LastDataRevision: r.LastDataRevision, DataCutoff: r.DataCutoff, WorkerStatus: r.WorkerStatus, OutboxLagSeconds: r.OutboxLagSeconds, ObservedAt: r.ObservedAt},
	}
}

func (r *Store) GetHealth(ctx context.Context, bindingID string) (domain.BindingHealth, error) {
	var health domain.BindingHealth
	if err := r.db.WithContext(ctx).Where("c_binding_id=?", bindingID).First(&health).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.BindingHealth{BindingID: bindingID, Status: "unknown"}, nil
		}
		return health, err
	}
	return health, nil
}

func (r *Store) ListRuns(ctx context.Context, filter RunFilter, page Page) ([]domain.StrategyRun, int64, error) {
	p := page.normalized()
	query := r.db.WithContext(ctx).Where("c_binding_id=?", filter.BindingID)
	if !filter.From.IsZero() {
		query = query.Where("c_trigger_bar_time >= ?", filter.From.UTC().Format(time.RFC3339Nano))
	}
	if !filter.To.IsZero() {
		query = query.Where("c_trigger_bar_time < ?", filter.To.UTC().Format(time.RFC3339Nano))
	}
	var total int64
	if err := query.Model(&domain.StrategyRun{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var runs []domain.StrategyRun
	if err := query.Order("c_trigger_bar_time DESC").Offset((p.Number - 1) * p.Size).Limit(p.Size).Find(&runs).Error; err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}

func (r *Store) ListTargets(ctx context.Context, runID string, page Page) ([]domain.TargetWeight, int64, error) {
	p := page.normalized()
	var run domain.StrategyRun
	if err := r.db.WithContext(ctx).Where("c_run_id=?", runID).First(&run).Error; err != nil {
		return nil, 0, err
	}
	var targets []domain.TargetWeight
	var raw string
	if err := r.db.WithContext(ctx).Table("t_strategy_runs").Select("c_output_json").Where("c_run_id=?", runID).Scan(&raw).Error; err != nil {
		return nil, 0, err
	}
	var output domain.Output
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		return nil, 0, err
	}
	total := int64(len(output.Targets))
	var comparisons []domain.TargetComparison
	if err := r.db.WithContext(ctx).Where("c_run_id=?", runID).Find(&comparisons).Error; err != nil {
		return nil, 0, err
	}
	comparisonByInstrument := make(map[string]domain.TargetComparison, len(comparisons))
	for _, comparison := range comparisons {
		comparisonByInstrument[comparison.InstrumentID] = comparison
	}
	for i := range output.Targets {
		if comparison, ok := comparisonByInstrument[output.Targets[i].InstrumentID]; ok {
			output.Targets[i].PortfolioTarget = comparison.PortfolioTarget
			output.Targets[i].ActualPosition = comparison.ActualPosition
			output.Targets[i].Deviation = comparison.Deviation
			output.Targets[i].SourceTime = formatComparisonTime(comparison.SourceTime)
			output.Targets[i].DataRevision = comparison.DataRevision
		}
	}
	start := (p.Number - 1) * p.Size
	if start >= len(output.Targets) {
		return []domain.TargetWeight{}, total, nil
	}
	end := start + p.Size
	if end > len(output.Targets) {
		end = len(output.Targets)
	}
	targets = append(targets, output.Targets[start:end]...)
	return targets, total, nil
}

func formatComparisonTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (r *Store) ListPerformancePoints(ctx context.Context, filter PerformanceFilter) ([]domain.PerformancePoint, error) {
	if filter.Source != "" && !domain.ValidPerformanceSource(filter.Source) {
		return nil, fmt.Errorf("unsupported performance source %q", filter.Source)
	}
	query := r.db.WithContext(ctx).Where("c_binding_id=?", filter.BindingID)
	if filter.Source != "" {
		query = query.Where("c_performance_source=?", filter.Source)
	}
	if !filter.From.IsZero() {
		query = query.Where("c_point_time >= ?", filter.From)
	}
	if !filter.To.IsZero() {
		query = query.Where("c_point_time < ?", filter.To)
	}
	var points []domain.PerformancePoint
	// Keep the control-plane response bounded. Callers can request a narrower
	// time range for detailed inspection; the UI samples the returned series.
	if err := query.Order("c_point_time ASC").Limit(5000).Find(&points).Error; err != nil {
		return nil, err
	}
	return points, nil
}

func (r *Store) ListPerformanceDaily(ctx context.Context, filter PerformanceFilter) ([]domain.PerformanceDaily, error) {
	if filter.Source != "" && !domain.ValidPerformanceSource(filter.Source) {
		return nil, fmt.Errorf("unsupported performance source %q", filter.Source)
	}
	query := r.db.WithContext(ctx).Where("c_binding_id=?", filter.BindingID)
	if filter.Source != "" {
		query = query.Where("c_performance_source=?", filter.Source)
	}
	if !filter.From.IsZero() {
		query = query.Where("c_trade_date >= ?", filter.From.UTC().Format("2006-01-02"))
	}
	if !filter.To.IsZero() {
		query = query.Where("c_trade_date < ?", filter.To.UTC().Format("2006-01-02"))
	}
	var rows []domain.PerformanceDaily
	if err := query.Order("c_trade_date ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

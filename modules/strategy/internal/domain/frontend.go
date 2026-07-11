package domain

import (
	"errors"
	"fmt"
	"time"
)

var performanceSources = map[string]struct{}{
	"backtest": {}, "observe": {}, "paper": {}, "live": {},
}

type PerformancePoint struct {
	BindingID        string    `gorm:"column:c_binding_id"`
	Source           string    `gorm:"column:c_performance_source"`
	PointTime        time.Time `gorm:"column:c_point_time"`
	NAV              string    `gorm:"column:c_nav"`
	CumulativeReturn string    `gorm:"column:c_cumulative_return"`
	Drawdown         string    `gorm:"column:c_drawdown"`
	GrossExposure    string    `gorm:"column:c_gross_exposure"`
	NetExposure      string    `gorm:"column:c_net_exposure"`
	Turnover         string    `gorm:"column:c_turnover"`
	Fees             string    `gorm:"column:c_fees"`
	DataRevision     string    `gorm:"column:c_data_revision"`
	CalculatedAt     time.Time `gorm:"column:c_calculated_at"`
}

func (PerformancePoint) TableName() string { return "t_strategy_performance_points" }

func (p PerformancePoint) Validate() error {
	if p.BindingID == "" {
		return errors.New("performance point binding_id is required")
	}
	if _, ok := performanceSources[p.Source]; !ok {
		return fmt.Errorf("unsupported performance source %q", p.Source)
	}
	if p.PointTime.IsZero() {
		return errors.New("performance point time is required")
	}
	return nil
}

func ValidPerformanceSource(source string) bool {
	_, ok := performanceSources[source]
	return ok
}

type PerformanceDaily struct {
	BindingID    string    `gorm:"column:c_binding_id"`
	Source       string    `gorm:"column:c_performance_source"`
	TradeDate    string    `gorm:"column:c_trade_date"`
	StartNAV     string    `gorm:"column:c_start_nav"`
	EndNAV       string    `gorm:"column:c_end_nav"`
	Return       string    `gorm:"column:c_return"`
	MaxDrawdown  string    `gorm:"column:c_max_drawdown"`
	Turnover     string    `gorm:"column:c_turnover"`
	Fees         string    `gorm:"column:c_fees"`
	WinCount     int64     `gorm:"column:c_win_count"`
	LossCount    int64     `gorm:"column:c_loss_count"`
	SampleCount  int64     `gorm:"column:c_sample_count"`
	DataRevision string    `gorm:"column:c_data_revision"`
	CalculatedAt time.Time `gorm:"column:c_calculated_at"`
}

func (PerformanceDaily) TableName() string { return "t_strategy_performance_daily" }

type RunMetrics struct {
	RunID              string    `gorm:"column:c_run_id;primaryKey"`
	QueueDelayMS       int64     `gorm:"column:c_queue_delay_ms"`
	SnapshotDurationMS int64     `gorm:"column:c_snapshot_duration_ms"`
	ComputeDurationMS  int64     `gorm:"column:c_compute_duration_ms"`
	ValidateDurationMS int64     `gorm:"column:c_validate_duration_ms"`
	TotalDurationMS    int64     `gorm:"column:c_total_duration_ms"`
	InputRows          int64     `gorm:"column:c_input_rows"`
	OutputTargets      int64     `gorm:"column:c_output_targets"`
	WorkerID           string    `gorm:"column:c_worker_id"`
	CreateTime         time.Time `gorm:"column:c_ctime"`
}

func (RunMetrics) TableName() string { return "t_strategy_run_metrics" }

type BindingHealth struct {
	BindingID        string    `gorm:"column:c_binding_id;primaryKey"`
	Status           string    `gorm:"column:c_status"`
	Mode             string    `gorm:"column:c_mode"`
	LastRunID        string    `gorm:"column:c_last_run_id"`
	LastSuccessAt    time.Time `gorm:"column:c_last_success_at"`
	LastErrorType    string    `gorm:"column:c_last_error_type"`
	LastErrorMessage string    `gorm:"column:c_last_error_message"`
	LastDataRevision string    `gorm:"column:c_last_data_revision"`
	DataCutoff       time.Time `gorm:"column:c_data_cutoff"`
	WorkerStatus     string    `gorm:"column:c_worker_status"`
	OutboxLagSeconds int64     `gorm:"column:c_outbox_lag_seconds"`
	ObservedAt       time.Time `gorm:"column:c_observed_at"`
}

func (BindingHealth) TableName() string { return "t_strategy_binding_health" }

type OperationAudit struct {
	OperationID string    `gorm:"column:c_operation_id;primaryKey"`
	Operator    string    `gorm:"column:c_operator"`
	Action      string    `gorm:"column:c_action"`
	BindingID   string    `gorm:"column:c_binding_id"`
	OldValue    string    `gorm:"column:c_old_value"`
	NewValue    string    `gorm:"column:c_new_value"`
	Reason      string    `gorm:"column:c_reason"`
	RequestID   string    `gorm:"column:c_request_id"`
	CreateTime  time.Time `gorm:"column:c_ctime"`
}

func (OperationAudit) TableName() string { return "t_strategy_operation_audits" }

type RunningStrategySummary struct {
	StrategyID       string
	Version          string
	BindingID        string
	SpaceID          string
	ViewID           string
	Freq             string
	Mode             string
	Status           string
	SourceHash       string
	LastRunID        string
	LastRunAt        time.Time
	LastDataRevision string
	LastDurationMS   int64
	Health           BindingHealth
}

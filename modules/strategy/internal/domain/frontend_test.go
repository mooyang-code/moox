package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPerformancePoint_Validate_ValidPoint_ShouldReturnNil(t *testing.T) {
	point := PerformancePoint{
		BindingID: "binding-1",
		Source:    "backtest",
		PointTime: time.Now(),
	}
	assert.NoError(t, point.Validate())
}

func TestPerformancePoint_Validate_MissingBindingID_ShouldReturnError(t *testing.T) {
	point := PerformancePoint{
		Source:    "paper",
		PointTime: time.Now(),
	}
	err := point.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "binding_id is required")
}

func TestPerformancePoint_Validate_UnsupportedSource_ShouldReturnError(t *testing.T) {
	point := PerformancePoint{
		BindingID: "binding-1",
		Source:    "unknown",
		PointTime: time.Now(),
	}
	err := point.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported performance source")
}

func TestPerformancePoint_Validate_ZeroPointTime_ShouldReturnError(t *testing.T) {
	point := PerformancePoint{
		BindingID: "binding-1",
		Source:    "live",
	}
	err := point.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "time is required")
}

func TestPerformancePoint_ValidPerformanceSource_KnownSource_ShouldReturnTrue(t *testing.T) {
	assert.True(t, ValidPerformanceSource("observe"))
}

func TestPerformancePoint_ValidPerformanceSource_UnknownSource_ShouldReturnFalse(t *testing.T) {
	assert.False(t, ValidPerformanceSource("unknown"))
}

func TestPerformancePoint_TableName_ShouldReturnPerformancePointsTable(t *testing.T) {
	assert.Equal(t, "t_strategy_performance_points", PerformancePoint{}.TableName())
}

func TestPerformanceDaily_TableName_ShouldReturnPerformanceDailyTable(t *testing.T) {
	assert.Equal(t, "t_strategy_performance_daily", PerformanceDaily{}.TableName())
}

func TestRunMetrics_TableName_ShouldReturnRunMetricsTable(t *testing.T) {
	assert.Equal(t, "t_strategy_run_metrics", RunMetrics{}.TableName())
}

func TestBindingHealth_TableName_ShouldReturnBindingHealthTable(t *testing.T) {
	assert.Equal(t, "t_strategy_binding_health", BindingHealth{}.TableName())
}

func TestOperationAudit_TableName_ShouldReturnOperationAuditsTable(t *testing.T) {
	assert.Equal(t, "t_strategy_operation_audits", OperationAudit{}.TableName())
}

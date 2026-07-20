package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWebhookChannel_TableName_ShouldReturnMonitorWebhooksTable(t *testing.T) {
	assert.Equal(t, "t_monitor_webhooks", WebhookChannel{}.TableName())
}

func TestAlertRule_TableName_ShouldReturnMonitorAlertRulesTable(t *testing.T) {
	assert.Equal(t, "t_monitor_alert_rules", AlertRule{}.TableName())
}

func TestAlertState_TableName_ShouldReturnMonitorAlertStatesTable(t *testing.T) {
	assert.Equal(t, "t_monitor_alert_states", AlertState{}.TableName())
}

func TestAlertEvent_TableName_ShouldReturnMonitorAlertEventsTable(t *testing.T) {
	assert.Equal(t, "t_monitor_alert_events", AlertEvent{}.TableName())
}

func TestCheckResult_TableName_ShouldReturnMonitorCheckResultsTable(t *testing.T) {
	assert.Equal(t, "t_monitor_check_results", CheckResult{}.TableName())
}

func TestCheck_TableName_ShouldReturnMonitorChecksTable(t *testing.T) {
	assert.Equal(t, "t_monitor_checks", Check{}.TableName())
}

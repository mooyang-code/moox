package rpc

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetInfoHelpers(t *testing.T) {
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, success().GetCode())
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, invalid(assert.AnError).GetCode())
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, inner(assert.AnError).GetCode())
	assert.Equal(t, commonpb.ErrorCode_NOT_FOUND, notFound("missing").GetCode())
}

func TestCheckToPBMapsFields(t *testing.T) {
	checked := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	next := checked.Add(time.Minute)
	check := domain.Check{
		SpaceID: "space-a", CheckID: "check-a", Name: "api", GroupName: "core",
		Kind: domain.CheckKindHTTP, URL: "https://example.com", Method: "GET",
		IntervalSeconds: 60, TimeoutMS: 1000, Enabled: true,
		LastCheckedAt: &checked, NextCheckAt: &next,
		CreatedAt: checked, UpdatedAt: checked,
	}
	pb := checkToPB(check)
	require.NotNil(t, pb)
	assert.Equal(t, "space-a", pb.GetSpaceId())
	assert.Equal(t, monitorpb.CheckKind_CHECK_KIND_HTTP, pb.GetKind())
	assert.Equal(t, checked.UTC().Format(time.RFC3339Nano), pb.GetLastCheckedAt())
}

func TestKindAndStatusMappings(t *testing.T) {
	assert.Equal(t, domain.CheckKindHTTP, kindToString(monitorpb.CheckKind_CHECK_KIND_HTTP))
	assert.Equal(t, domain.CheckKindTCP, kindToString(monitorpb.CheckKind_CHECK_KIND_TCP))
	assert.Equal(t, "", kindToString(monitorpb.CheckKind(999)))
	assert.Equal(t, monitorpb.CheckKind_CHECK_KIND_HTTP, kindToPB(domain.CheckKindHTTP))
	assert.Equal(t, monitorpb.CheckKind_CHECK_KIND_TCP, kindToPB(domain.CheckKindTCP))
	assert.Equal(t, monitorpb.CheckKind_CHECK_KIND_UNSPECIFIED, kindToPB("unknown"))
	assert.Equal(t, monitorpb.CheckStatus_CHECK_STATUS_OK, checkStatusToPB(domain.CheckStatusOK))
	assert.Equal(t, monitorpb.AlertStatus_ALERT_STATUS_FIRING, alertStatusToPB(domain.AlertStatusFiring))
	assert.Equal(t, monitorpb.AlertEventType_ALERT_EVENT_TYPE_RESOLVED, alertEventTypeToPB(domain.AlertEventResolved))
}

func TestTimeHelpers(t *testing.T) {
	assert.Equal(t, "", timeToString(time.Time{}))
	assert.Equal(t, "", timePtrToString(nil))
	ts := time.Date(2026, 3, 1, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))
	assert.Equal(t, ts.UTC().Format(time.RFC3339Nano), timePtrToString(&ts))
}

func TestEntityConverters(t *testing.T) {
	now := time.Now().UTC()
	result := resultToPB(domain.CheckResult{ResultID: "r1", SpaceID: "s", CheckID: "c", Success: true, Status: domain.CheckStatusOK, CheckedAt: now})
	assert.True(t, result.GetSuccess())
	webhook := webhookToPB(domain.WebhookChannel{SpaceID: "s", WebhookID: "w", URL: "http://hook"})
	assert.Equal(t, "w", webhook.GetWebhookId())
	rule := ruleToPB(domain.AlertRule{SpaceID: "s", RuleID: "r", FailureThreshold: 3})
	assert.Equal(t, int32(3), rule.GetFailureThreshold())
	event := eventToPB(domain.AlertEvent{EventID: "e", EventType: domain.AlertEventTriggered, Status: domain.AlertStatusFiring, CreatedAt: now})
	assert.Equal(t, monitorpb.AlertEventType_ALERT_EVENT_TYPE_TRIGGERED, event.GetEventType())
	instance := instanceToPB(domain.MonitorInstance{InstanceID: "i", IsLocal: true})
	assert.True(t, instance.GetIsLocal())
}

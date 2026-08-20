package rpc

import (
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

func retInfo(code commonpb.ErrorCode, msg string) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: code, Msg: msg}
}

func success() *commonpb.RetInfo {
	return retInfo(commonpb.ErrorCode_SUCCESS, "")
}

func invalid(err error) *commonpb.RetInfo {
	return retInfo(commonpb.ErrorCode_INVALID_PARAM, err.Error())
}

func inner(err error) *commonpb.RetInfo {
	return retInfo(commonpb.ErrorCode_INNER_ERR, err.Error())
}

func notFound(msg string) *commonpb.RetInfo {
	return retInfo(commonpb.ErrorCode_NOT_FOUND, msg)
}

func checkToPB(check domain.Check) *monitorpb.MonitorCheck {
	return &monitorpb.MonitorCheck{
		SpaceId:         check.SpaceID,
		CheckId:         check.CheckID,
		Name:            check.Name,
		GroupName:       check.GroupName,
		Kind:            kindToPB(check.Kind),
		Url:             check.URL,
		Method:          check.Method,
		HeadersJson:     check.Headers,
		Body:            check.Body,
		TcpHost:         check.TCPHost,
		TcpPort:         int32(check.TCPPort),
		IntervalSeconds: int32(check.IntervalSeconds),
		TimeoutMs:       int32(check.TimeoutMS),
		ExpectedStatus:  check.ExpectedStatus,
		MaxResponseMs:   int32(check.MaxResponseMS),
		BodyContains:    check.BodyContains,
		Enabled:         check.Enabled,
		Source:          check.Source,
		LabelsJson:      check.Labels,
		Description:     check.Description,
		LastCheckedAt:   timePtrToString(check.LastCheckedAt),
		NextCheckAt:     timePtrToString(check.NextCheckAt),
		CreatedAt:       timeToString(check.CreatedAt),
		UpdatedAt:       timeToString(check.UpdatedAt),
	}
}

func resultToPB(result domain.CheckResult) *monitorpb.CheckResult {
	return &monitorpb.CheckResult{
		ResultId:     result.ResultID,
		SpaceId:      result.SpaceID,
		CheckId:      result.CheckID,
		InstanceId:   result.InstanceID,
		Success:      result.Success,
		Status:       checkStatusToPB(result.Status),
		HttpStatus:   int32(result.HTTPStatus),
		Connected:    result.Connected,
		LatencyMs:    result.LatencyMS,
		ErrorMessage: result.ErrorMessage,
		BodyExcerpt:  result.BodyExcerpt,
		CheckedAt:    timeToString(result.CheckedAt),
	}
}

func webhookToPB(webhook domain.WebhookChannel) *monitorpb.WebhookChannel {
	return &monitorpb.WebhookChannel{
		SpaceId:      webhook.SpaceID,
		WebhookId:    webhook.WebhookID,
		Name:         webhook.Name,
		Url:          webhook.URL,
		Method:       webhook.Method,
		HeadersJson:  webhook.Headers,
		BodyTemplate: webhook.BodyTemplate,
		Enabled:      webhook.Enabled,
		CreatedAt:    timeToString(webhook.CreatedAt),
		UpdatedAt:    timeToString(webhook.UpdatedAt),
	}
}

func ruleToPB(rule domain.AlertRule) *monitorpb.AlertRule {
	return &monitorpb.AlertRule{
		SpaceId:                        rule.SpaceID,
		RuleId:                         rule.RuleID,
		CheckId:                        rule.CheckID,
		WebhookId:                      rule.WebhookID,
		FailureThreshold:               int32(rule.FailureThreshold),
		SuccessThreshold:               int32(rule.SuccessThreshold),
		MinimumReminderIntervalSeconds: int32(rule.MinimumReminderIntervalSeconds),
		SendOnResolved:                 rule.SendOnResolved,
		Enabled:                        rule.Enabled,
		Description:                    rule.Description,
		CreatedAt:                      timeToString(rule.CreatedAt),
		UpdatedAt:                      timeToString(rule.UpdatedAt),
	}
}

func eventToPB(event domain.AlertEvent) *monitorpb.AlertEvent {
	return &monitorpb.AlertEvent{
		EventId:     event.EventID,
		SpaceId:     event.SpaceID,
		RuleId:      event.RuleID,
		CheckId:     event.CheckID,
		EventType:   alertEventTypeToPB(event.EventType),
		Status:      alertStatusToPB(event.Status),
		Message:     event.Message,
		PayloadJson: event.Payload,
		CreatedAt:   timeToString(event.CreatedAt),
	}
}

func kindToString(kind monitorpb.CheckKind) string {
	switch kind {
	case monitorpb.CheckKind_CHECK_KIND_HTTP:
		return domain.CheckKindHTTP
	case monitorpb.CheckKind_CHECK_KIND_TCP:
		return domain.CheckKindTCP
	case monitorpb.CheckKind_CHECK_KIND_EXTERNAL:
		return domain.CheckKindExternal
	default:
		return ""
	}
}

func kindToPB(kind string) monitorpb.CheckKind {
	switch strings.ToLower(kind) {
	case domain.CheckKindHTTP:
		return monitorpb.CheckKind_CHECK_KIND_HTTP
	case domain.CheckKindTCP:
		return monitorpb.CheckKind_CHECK_KIND_TCP
	case domain.CheckKindExternal:
		return monitorpb.CheckKind_CHECK_KIND_EXTERNAL
	default:
		return monitorpb.CheckKind_CHECK_KIND_UNSPECIFIED
	}
}

func checkStatusToPB(status string) monitorpb.CheckStatus {
	switch status {
	case domain.CheckStatusOK:
		return monitorpb.CheckStatus_CHECK_STATUS_OK
	case domain.CheckStatusDegraded:
		return monitorpb.CheckStatus_CHECK_STATUS_DEGRADED
	case domain.CheckStatusDown:
		return monitorpb.CheckStatus_CHECK_STATUS_DOWN
	default:
		return monitorpb.CheckStatus_CHECK_STATUS_UNSPECIFIED
	}
}

func alertStatusToPB(status string) monitorpb.AlertStatus {
	switch status {
	case domain.AlertStatusOK:
		return monitorpb.AlertStatus_ALERT_STATUS_OK
	case domain.AlertStatusFiring:
		return monitorpb.AlertStatus_ALERT_STATUS_FIRING
	case domain.AlertStatusResolved:
		return monitorpb.AlertStatus_ALERT_STATUS_RESOLVED
	default:
		return monitorpb.AlertStatus_ALERT_STATUS_UNSPECIFIED
	}
}

func alertEventTypeToPB(eventType string) monitorpb.AlertEventType {
	switch eventType {
	case domain.AlertEventTriggered:
		return monitorpb.AlertEventType_ALERT_EVENT_TYPE_TRIGGERED
	case domain.AlertEventReminder:
		return monitorpb.AlertEventType_ALERT_EVENT_TYPE_REMINDER
	case domain.AlertEventResolved:
		return monitorpb.AlertEventType_ALERT_EVENT_TYPE_RESOLVED
	default:
		return monitorpb.AlertEventType_ALERT_EVENT_TYPE_UNSPECIFIED
	}
}

func timeToString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func timePtrToString(t *time.Time) string {
	if t == nil {
		return ""
	}
	return timeToString(*t)
}

package rpc

import (
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

func retInfo(code commonpb.ErrorCode, msg string) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: code, Msg: msg}
}

func success() *commonpb.RetInfo { return retInfo(commonpb.ErrorCode_SUCCESS, "") }

func invalid(err error) *commonpb.RetInfo {
	return retInfo(commonpb.ErrorCode_INVALID_PARAM, err.Error())
}

func inner(err error) *commonpb.RetInfo { return retInfo(commonpb.ErrorCode_INNER_ERR, err.Error()) }

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

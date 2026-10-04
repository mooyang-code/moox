package alerting

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/notification"
	"gorm.io/gorm"
)

type Event struct {
	EventID   string
	EventType string
	Status    string
	Message   string
	Check     domain.Check
	Result    domain.CheckResult
	Rule      domain.AlertRule
	DedupeKey string
}

type Options struct {
	Channel func(context.Context) (*domain.NotificationChannel, error)
	Now     func() time.Time
}

type Evaluator struct {
	alerts  *store.AlertRepository
	channel func(context.Context) (*domain.NotificationChannel, error)
	now     func() time.Time
}

func NewEvaluator(alerts *store.AlertRepository, opts Options) *Evaluator {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Evaluator{
		alerts:  alerts,
		channel: opts.Channel,
		now:     now,
	}
}

func (e *Evaluator) Evaluate(ctx context.Context, check domain.Check, result domain.CheckResult) error {
	rules, err := e.alerts.ListEnabledRulesForCheck(ctx, check.SpaceID, check.CheckID)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if err := e.evaluateRule(ctx, check, result, rule); err != nil {
			return err
		}
	}
	return nil
}

func (e *Evaluator) evaluateRule(ctx context.Context, check domain.Check, result domain.CheckResult, rule domain.AlertRule) error {
	state, err := e.alerts.GetState(ctx, rule.SpaceID, rule.RuleID, rule.CheckID)
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return err
		}
		state = &domain.AlertState{
			SpaceID:   rule.SpaceID,
			RuleID:    rule.RuleID,
			CheckID:   rule.CheckID,
			Status:    domain.AlertStatusOK,
			DedupeKey: dedupeKey(rule, check),
		}
	}
	if result.Success {
		return e.evaluateSuccess(ctx, check, result, rule, state)
	}
	return e.evaluateFailure(ctx, check, result, rule, state)
}

func (e *Evaluator) evaluateFailure(ctx context.Context, check domain.Check, result domain.CheckResult, rule domain.AlertRule, state *domain.AlertState) error {
	now := e.now()
	state.FailureCount++
	state.SuccessCount = 0
	state.DedupeKey = dedupeKey(rule, check)

	if state.Status != domain.AlertStatusFiring && state.FailureCount >= rule.FailureThreshold {
		state.Status = domain.AlertStatusFiring
		state.TriggeredAt = &now
		state.ResolvedAt = nil
		state.LastReminderAt = &now
		if err := e.alerts.UpsertState(ctx, state); err != nil {
			return err
		}
		if err := e.recordAndSend(ctx, check, result, rule, state, domain.AlertEventTriggered); err != nil {
			// Do not suppress the next sample after a transient notification
			// failure. A nil reminder timestamp makes the next firing result a
			// retry instead of waiting for the normal five-minute reminder.
			state.LastReminderAt = nil
			_ = e.alerts.UpsertState(ctx, state)
			return err
		}
	} else if state.Status == domain.AlertStatusFiring && reminderDue(state.LastReminderAt, now, rule.MinimumReminderIntervalSeconds) {
		state.LastReminderAt = &now
		if err := e.alerts.UpsertState(ctx, state); err != nil {
			return err
		}
		if err := e.recordAndSend(ctx, check, result, rule, state, domain.AlertEventReminder); err != nil {
			state.LastReminderAt = nil
			_ = e.alerts.UpsertState(ctx, state)
			return err
		}
	}
	return e.alerts.UpsertState(ctx, state)
}

func (e *Evaluator) evaluateSuccess(ctx context.Context, check domain.Check, result domain.CheckResult, rule domain.AlertRule, state *domain.AlertState) error {
	now := e.now()
	state.SuccessCount++
	state.FailureCount = 0
	state.DedupeKey = dedupeKey(rule, check)
	if state.Status == domain.AlertStatusFiring && state.SuccessCount >= rule.SuccessThreshold {
		state.Status = domain.AlertStatusResolved
		state.ResolvedAt = &now
		if rule.SendOnResolved {
			if err := e.recordAndSend(ctx, check, result, rule, state, domain.AlertEventResolved); err != nil {
				// Keep the alert firing until the recovery notification is
				// delivered; a later healthy sample retries the notification.
				state.Status = domain.AlertStatusFiring
				state.ResolvedAt = nil
				state.SuccessCount = 0
				state.LastReminderAt = nil
				return err
			}
			return e.alerts.UpsertState(ctx, state)
		}
		if err := e.alerts.UpsertState(ctx, state); err != nil {
			return err
		} else if err := e.recordEvent(ctx, check, result, rule, state, domain.AlertEventResolved, ""); err != nil {
			return err
		}
	}
	return e.alerts.UpsertState(ctx, state)
}

func (e *Evaluator) recordAndSend(ctx context.Context, check domain.Check, result domain.CheckResult, rule domain.AlertRule, state *domain.AlertState, eventType string) error {
	event := Event{
		EventID:   newEventID(),
		EventType: eventType,
		Status:    state.Status,
		Message:   eventMessage(eventType, check, result),
		Check:     check,
		Result:    result,
		Rule:      rule,
		DedupeKey: state.DedupeKey,
	}
	if err := e.recordEventObject(ctx, event); err != nil {
		return err
	}
	var sendErr error
	if e.channel != nil {
		channel, err := e.channel(ctx)
		if err != nil {
			return e.recordSendFailure(ctx, check, result, rule, state, err)
		}
		if channel == nil || strings.TrimSpace(channel.WebhookURL) == "" {
			return nil
		}
		sender, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(channel.ChannelType), WebhookURL: channel.WebhookURL})
		if err != nil {
			return e.recordSendFailure(ctx, check, result, rule, state, err)
		}
		sendErr = sender.Send(ctx, notification.Message{Key: event.DedupeKey, Severity: notificationSeverity(eventType), Title: event.Check.Name, Body: event.Message})
	}
	if sendErr != nil {
		return e.recordSendFailure(ctx, check, result, rule, state, fmt.Errorf("send alert notification: %w", sendErr))
	}
	return nil
}

func (e *Evaluator) recordSendFailure(ctx context.Context, check domain.Check, result domain.CheckResult, rule domain.AlertRule, state *domain.AlertState, sendErr error) error {
	if sendErr == nil {
		return nil
	}
	if recordErr := e.recordEvent(ctx, check, result, rule, state, domain.AlertEventSendFailed, "通知发送失败："+sendErr.Error()); recordErr != nil {
		return errors.Join(sendErr, recordErr)
	}
	return sendErr
}

func notificationSeverity(eventType string) notification.Severity {
	switch eventType {
	case domain.AlertEventResolved:
		return notification.SeverityInfo
	case domain.AlertEventReminder:
		return notification.SeverityWarning
	default:
		return notification.SeverityCritical
	}
}

func (e *Evaluator) recordEvent(ctx context.Context, check domain.Check, result domain.CheckResult, rule domain.AlertRule, state *domain.AlertState, eventType, message string) error {
	if message == "" {
		message = eventMessage(eventType, check, result)
	}
	return e.recordEventObject(ctx, Event{
		EventID:   newEventID(),
		EventType: eventType,
		Status:    state.Status,
		Message:   message,
		Check:     check,
		Result:    result,
		Rule:      rule,
		DedupeKey: state.DedupeKey,
	})
}

func (e *Evaluator) recordEventObject(ctx context.Context, event Event) error {
	return e.alerts.CreateEvent(ctx, &domain.AlertEvent{
		EventID:   event.EventID,
		SpaceID:   event.Rule.SpaceID,
		RuleID:    event.Rule.RuleID,
		CheckID:   event.Check.CheckID,
		EventType: event.EventType,
		Status:    event.Status,
		Message:   event.Message,
		Payload:   "{}",
		CreatedAt: e.now(),
	})
}

func reminderDue(last *time.Time, now time.Time, intervalSeconds int) bool {
	if intervalSeconds <= 0 {
		return false
	}
	if last == nil {
		return true
	}
	return now.Sub(*last) >= time.Duration(intervalSeconds)*time.Second
}

func dedupeKey(rule domain.AlertRule, check domain.Check) string {
	return rule.RuleID + ":" + check.CheckID
}

func eventMessage(eventType string, check domain.Check, result domain.CheckResult) string {
	name := strings.TrimSpace(check.Name)
	if name == "" {
		name = check.CheckID
	}
	status := map[string]string{
		domain.AlertEventTriggered:  "触发告警",
		domain.AlertEventReminder:   "告警提醒",
		domain.AlertEventResolved:   "告警恢复",
		domain.AlertEventSendFailed: "通知发送失败",
	}[eventType]
	if status == "" {
		status = eventType
	}
	parts := []string{fmt.Sprintf("监控项：%s", name), fmt.Sprintf("状态：%s", status)}
	if result.ErrorMessage != "" {
		parts = append(parts, "原因："+result.ErrorMessage)
	}
	if !result.CheckedAt.IsZero() {
		parts = append(parts, "检查时间："+result.CheckedAt.UTC().Format(time.RFC3339))
	}
	if check.Description != "" {
		parts = append(parts, "建议："+strings.TrimSpace(check.Description))
	}
	return strings.Join(parts, "；")
}

func newEventID() string { return uuid.NewString() }

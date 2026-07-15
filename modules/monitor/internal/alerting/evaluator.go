package alerting

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"gorm.io/gorm"
)

type Event struct {
	EventID         string
	EventType       string
	Status          string
	OwnerInstanceID string
	Message         string
	Check           domain.Check
	Result          domain.CheckResult
	Rule            domain.AlertRule
	DedupeKey       string
}

type Notifier interface {
	Send(context.Context, domain.WebhookChannel, Event) error
}

type Options struct {
	InstanceID string
	Notifier   Notifier
	Now        func() time.Time
}

type Evaluator struct {
	alerts   *store.AlertRepository
	instance string
	notifier Notifier
	now      func() time.Time
}

func NewEvaluator(alerts *store.AlertRepository, opts Options) *Evaluator {
	instance := opts.InstanceID
	if instance == "" {
		instance = "monitor"
	}
	notifier := opts.Notifier
	if notifier == nil {
		notifier = WebhookNotifier{}
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Evaluator{
		alerts:   alerts,
		instance: instance,
		notifier: notifier,
		now:      now,
	}
}

func (e *Evaluator) Evaluate(ctx context.Context, check domain.Check, result domain.CheckResult, activeInstanceIDs []string) error {
	rules, err := e.alerts.ListEnabledRulesForCheck(ctx, check.SpaceID, check.CheckID)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if err := e.evaluateRule(ctx, check, result, rule, activeInstanceIDs); err != nil {
			return err
		}
	}
	return nil
}

func (e *Evaluator) evaluateRule(ctx context.Context, check domain.Check, result domain.CheckResult, rule domain.AlertRule, activeInstanceIDs []string) error {
	owner := Owner(check.CheckID, rule.RuleID, activeInstanceIDs)
	if owner != "" && owner != e.instance {
		return nil
	}
	state, err := e.alerts.GetState(ctx, rule.SpaceID, rule.RuleID, rule.CheckID)
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return err
		}
		state = &domain.AlertState{
			SpaceID:         rule.SpaceID,
			RuleID:          rule.RuleID,
			CheckID:         rule.CheckID,
			Status:          domain.AlertStatusOK,
			OwnerInstanceID: e.instance,
			DedupeKey:       dedupeKey(rule, check),
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
	state.OwnerInstanceID = e.instance
	state.DedupeKey = dedupeKey(rule, check)

	if state.Status != domain.AlertStatusFiring && state.FailureCount >= rule.FailureThreshold {
		state.Status = domain.AlertStatusFiring
		state.TriggeredAt = &now
		state.ResolvedAt = nil
		state.LastReminderAt = &now
		if err := e.recordAndSend(ctx, check, result, rule, state, domain.AlertEventTriggered); err != nil {
			return err
		}
	} else if state.Status == domain.AlertStatusFiring && reminderDue(state.LastReminderAt, now, rule.MinimumReminderIntervalSeconds) {
		state.LastReminderAt = &now
		if err := e.recordAndSend(ctx, check, result, rule, state, domain.AlertEventReminder); err != nil {
			return err
		}
	}
	return e.alerts.UpsertState(ctx, state)
}

func (e *Evaluator) evaluateSuccess(ctx context.Context, check domain.Check, result domain.CheckResult, rule domain.AlertRule, state *domain.AlertState) error {
	now := e.now()
	state.SuccessCount++
	state.FailureCount = 0
	state.OwnerInstanceID = e.instance
	state.DedupeKey = dedupeKey(rule, check)
	if state.Status == domain.AlertStatusFiring && state.SuccessCount >= rule.SuccessThreshold {
		state.Status = domain.AlertStatusResolved
		state.ResolvedAt = &now
		if rule.SendOnResolved {
			if err := e.recordAndSend(ctx, check, result, rule, state, domain.AlertEventResolved); err != nil {
				return err
			}
		} else if err := e.recordEvent(ctx, check, result, rule, state, domain.AlertEventResolved, ""); err != nil {
			return err
		}
	}
	return e.alerts.UpsertState(ctx, state)
}

func (e *Evaluator) recordAndSend(ctx context.Context, check domain.Check, result domain.CheckResult, rule domain.AlertRule, state *domain.AlertState, eventType string) error {
	event := Event{
		EventID:         newEventID(),
		EventType:       eventType,
		Status:          state.Status,
		OwnerInstanceID: state.OwnerInstanceID,
		Message:         eventMessage(eventType, check, result),
		Check:           check,
		Result:          result,
		Rule:            rule,
		DedupeKey:       state.DedupeKey,
	}
	if err := e.recordEventObject(ctx, event); err != nil {
		return err
	}
	if strings.TrimSpace(rule.WebhookID) == "" {
		return nil
	}
	webhook, err := e.alerts.GetWebhook(ctx, rule.SpaceID, rule.WebhookID)
	if err != nil {
		return err
	}
	if err := e.notifier.Send(ctx, *webhook, event); err != nil {
		return e.recordEvent(ctx, check, result, rule, state, domain.AlertEventSendFailed, err.Error())
	}
	return nil
}

func (e *Evaluator) recordEvent(ctx context.Context, check domain.Check, result domain.CheckResult, rule domain.AlertRule, state *domain.AlertState, eventType, message string) error {
	if message == "" {
		message = eventMessage(eventType, check, result)
	}
	return e.recordEventObject(ctx, Event{
		EventID:         newEventID(),
		EventType:       eventType,
		Status:          state.Status,
		OwnerInstanceID: state.OwnerInstanceID,
		Message:         message,
		Check:           check,
		Result:          result,
		Rule:            rule,
		DedupeKey:       state.DedupeKey,
	})
}

func (e *Evaluator) recordEventObject(ctx context.Context, event Event) error {
	return e.alerts.CreateEvent(ctx, &domain.AlertEvent{
		EventID:         event.EventID,
		SpaceID:         event.Rule.SpaceID,
		RuleID:          event.Rule.RuleID,
		CheckID:         event.Check.CheckID,
		EventType:       event.EventType,
		Status:          event.Status,
		OwnerInstanceID: event.OwnerInstanceID,
		Message:         event.Message,
		Payload:         "{}",
		CreatedAt:       e.now(),
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
	if result.ErrorMessage != "" {
		return fmt.Sprintf("%s %s: %s", check.CheckID, eventType, result.ErrorMessage)
	}
	return fmt.Sprintf("%s %s", check.CheckID, eventType)
}

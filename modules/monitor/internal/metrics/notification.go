package metrics

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

type MetricEvent struct {
	EventType  string
	DedupeKey  string
	Rule       *monitorpb.MetricRule
	Evaluation *RuleEvaluation
}

// WebhookMetricNotifier adapts the existing webhook implementation so metric
// alerts retain the same timeout, retry and template semantics as check alerts.
type WebhookMetricNotifier struct{ Sender alerting.Notifier }

func (n WebhookMetricNotifier) SendMetric(ctx context.Context, webhook domain.WebhookChannel, event MetricEvent) error {
	sender := n.Sender
	if sender == nil {
		sender = alerting.WebhookNotifier{}
	}
	payload, _ := json.Marshal(event.Evaluation)
	check := domain.Check{CheckID: event.Rule.GetRuleId(), Name: event.Rule.GetName(), SpaceID: event.Rule.GetSpaceId()}
	result := domain.CheckResult{ErrorMessage: string(payload)}
	dedupeKey := event.DedupeKey
	if dedupeKey == "" {
		dedupeKey = fmt.Sprintf("%s:%s:%s", event.Rule.GetSpaceId(), event.Rule.GetRuleId(), event.EventType)
	}
	legacy := alerting.Event{EventID: dedupeKey, EventType: event.EventType, Status: event.Evaluation.Status, Message: fmt.Sprintf("metric rule %s %s", event.Rule.GetRuleId(), event.EventType), Check: check, Result: result, DedupeKey: dedupeKey}
	return sender.Send(ctx, webhook, legacy)
}

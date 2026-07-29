package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"

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

// WebhookMetricNotifier routes metric alerts through the shared webhook
// delivery implementation.
type WebhookMetricNotifier struct{ Sender alerting.Notifier }

func (n WebhookMetricNotifier) SendMetric(ctx context.Context, webhook domain.WebhookChannel, event MetricEvent) error {
	sender := n.Sender
	if sender == nil {
		sender = alerting.WebhookNotifier{}
	}
	check := domain.Check{CheckID: event.Rule.GetRuleId(), Name: event.Rule.GetName(), SpaceID: event.Rule.GetSpaceId()}
	checkedAt := event.Evaluation.EvaluatedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	result := domain.CheckResult{ErrorMessage: metricEvaluationMessage(event.Evaluation), CheckedAt: checkedAt}
	dedupeKey := event.DedupeKey
	if dedupeKey == "" {
		dedupeKey = fmt.Sprintf("%s:%s:%s", event.Rule.GetSpaceId(), event.Rule.GetRuleId(), event.EventType)
	}
	alert := alerting.Event{EventID: dedupeKey, EventType: event.EventType, Status: event.Evaluation.Status, Message: fmt.Sprintf("metric rule %s %s", event.Rule.GetRuleId(), event.EventType), Check: check, Result: result, DedupeKey: dedupeKey}
	return sender.Send(ctx, webhook, alert)
}

func metricEvaluationMessage(evaluation *RuleEvaluation) string {
	if evaluation == nil {
		return "指标规则评估结果不可用"
	}
	details := make([]string, 0, len(evaluation.Conditions))
	for _, condition := range evaluation.Conditions {
		if !condition.Result && condition.HasData {
			continue
		}
		if !condition.HasData {
			reason := strings.TrimSpace(condition.NoDataReason)
			if reason == "" {
				reason = "没有可用数据"
			}
			details = append(details, fmt.Sprintf("条件 %s：%s", condition.ConditionID, reason))
			continue
		}
		details = append(details, fmt.Sprintf(
			"条件 %s：当前值 %v，阈值 %v",
			condition.ConditionID, condition.Value, condition.Threshold,
		))
	}
	if len(details) == 0 {
		if evaluation.Status == domain.AlertStatusResolved || evaluation.Status == domain.AlertStatusOK {
			return "指标规则已恢复正常"
		}
		return "指标规则判定异常"
	}
	return strings.Join(details, "；")
}

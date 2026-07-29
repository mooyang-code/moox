package metrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

type captureMetricSender struct{ event alerting.Event }

func (s *captureMetricSender) Send(_ context.Context, _ domain.WebhookChannel, event alerting.Event) error {
	s.event = event
	return nil
}

func TestWebhookMetricNotifierUsesTransitionDedupeKey(t *testing.T) {
	sender := &captureMetricSender{}
	n := WebhookMetricNotifier{Sender: sender}
	rule := &monitorpb.MetricRule{SpaceId: "moox_system", RuleId: "rule-1", Name: "high"}
	eval := &RuleEvaluation{EvaluationID: "random-evaluation-id", Status: domain.AlertStatusFiring}
	if err := n.SendMetric(context.Background(), domain.WebhookChannel{Enabled: true}, MetricEvent{EventType: domain.AlertEventTriggered, DedupeKey: "moox_system:rule-1:triggered:123", Rule: rule, Evaluation: eval}); err != nil {
		t.Fatal(err)
	}
	if sender.event.DedupeKey != "moox_system:rule-1:triggered:123" || sender.event.EventID != sender.event.DedupeKey {
		t.Fatalf("event dedupe key=%q event id=%q", sender.event.DedupeKey, sender.event.EventID)
	}
	if sender.event.Result.CheckedAt.IsZero() {
		t.Fatal("metric notification has a zero checked_at")
	}
	if strings.HasPrefix(sender.event.Result.ErrorMessage, "{") {
		t.Fatalf("metric notification leaks raw JSON: %q", sender.event.Result.ErrorMessage)
	}
}

func TestMetricEvaluationMessageExplainsTriggeredCondition(t *testing.T) {
	evaluation := &RuleEvaluation{
		Status: domain.AlertStatusFiring, EvaluatedAt: time.Now().UTC(),
		Conditions: []ConditionResult{{ConditionID: "A", Value: 95, Threshold: 90, HasData: true, Result: true}},
	}
	message := metricEvaluationMessage(evaluation)
	if !strings.Contains(message, "条件 A") || !strings.Contains(message, "当前值 95") || !strings.Contains(message, "阈值 90") {
		t.Fatalf("metricEvaluationMessage() = %q", message)
	}
}

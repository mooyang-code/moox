package hostmetrics

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
)

func TestHostValuesAggregatesEntitiesAndUnavailableRates(t *testing.T) {
	values := hostValues(&hostmetricpb.HostSnapshot{
		Cpu:         &hostmetricpb.CpuMetric{UsageAvailable: false, UsagePercent: 99},
		Memory:      &hostmetricpb.MemoryMetric{UsagePercent: 50},
		Filesystems: []*hostmetricpb.FilesystemMetric{{UsagePercent: 40}, {UsagePercent: 90}},
		Disks:       []*hostmetricpb.DiskMetric{{RateAvailable: true, UtilizationPercent: 20}, {RateAvailable: true, UtilizationPercent: 70}},
		Networks: []*hostmetricpb.NetworkMetric{
			{ErrorRateAvailable: true, ReceiveErrorsPerSecond: 1.25, TransmitErrorsPerSecond: 0.75},
			{ErrorRateAvailable: false, ReceiveErrorsTotal: 100},
		},
	})
	if values[HostMetricCPU].available {
		t.Fatal("unavailable CPU was treated as available")
	}
	if values[HostMetricFilesystemUsage].value != 90 || values[HostMetricDiskUtilization].value != 70 || values[HostMetricNetworkErrors].value != 2 {
		t.Fatalf("aggregated values=%v", values)
	}
}

func TestHostValuesDoesNotTreatCumulativeNetworkErrorsAsARate(t *testing.T) {
	values := hostValues(&hostmetricpb.HostSnapshot{Networks: []*hostmetricpb.NetworkMetric{{
		RateAvailable: true, ReceiveErrorsTotal: 100, TransmitErrorsTotal: 50,
		ErrorRateAvailable: false,
	}}})
	if values[HostMetricNetworkErrors].available {
		t.Fatalf("cumulative network errors were treated as an interval rate: %+v", values)
	}
}

func TestAlertDeduplicationMarksOnlyAfterSuccessfulTransition(t *testing.T) {
	evaluator := &AlertEvaluator{}
	if evaluator.isSeen("message-1", "rule-1") {
		t.Fatal("message unexpectedly seen")
	}
	evaluator.remember("message-1", "rule-1")
	if !evaluator.isSeen("message-1", "rule-1") {
		t.Fatal("message was not remembered")
	}
	threshold, recovery := hostThresholds(domain.AlertRule{Description: `{"threshold": 92, "recovery_threshold": 80}`}, HostMetricCPU)
	if threshold != 92 || recovery != 80 {
		t.Fatalf("thresholds=%v,%v", threshold, recovery)
	}
}

type retryingHostNotifier struct {
	attempts int
	fail     bool
	events   []string
}

func (n *retryingHostNotifier) Send(_ context.Context, _ domain.WebhookChannel, event alerting.Event) error {
	n.attempts++
	n.events = append(n.events, event.EventType)
	if n.fail {
		return errors.New("temporary webhook failure")
	}
	return nil
}

func TestHostAlertRetriesFailedNotificationAsReminder(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	notifier := &retryingHostNotifier{fail: true}
	evaluator := &AlertEvaluator{
		Repository: manager.Repositories().Alerts,
		Notifier:   notifier,
		Webhook: func(context.Context, string, string) (*domain.WebhookChannel, error) {
			return &domain.WebhookChannel{Enabled: true}, nil
		},
	}
	rule := domain.AlertRule{
		SpaceID: SpaceID, RuleID: "cpu-rule", CheckID: HostRuleKey("agent-a", HostMetricCPU),
		WebhookID: "wecom", FailureThreshold: 1, SuccessThreshold: 1,
		MinimumReminderIntervalSeconds: 300,
	}
	if err := evaluator.transition(t.Context(), rule, "agent-a", "message-1", true, 80, now, 95); err != nil {
		t.Fatal(err)
	}
	if notifier.attempts != 1 || notifier.events[0] != domain.AlertEventTriggered {
		t.Fatalf("initial notification = %+v", notifier)
	}
	if err := evaluator.transition(t.Context(), rule, "agent-a", "message-2", true, 80, now.Add(time.Minute), 96); err != nil {
		t.Fatal(err)
	}
	if notifier.attempts != 1 {
		t.Fatalf("early retry attempts = %d", notifier.attempts)
	}
	notifier.fail = false
	if err := evaluator.transition(t.Context(), rule, "agent-a", "message-3", true, 80, now.Add(301*time.Second), 97); err != nil {
		t.Fatal(err)
	}
	if notifier.attempts != 2 || notifier.events[1] != domain.AlertEventReminder {
		t.Fatalf("retry notification = %+v", notifier)
	}
	events, err := manager.Repositories().Alerts.ListEvents(t.Context(), SpaceID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
}

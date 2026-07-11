package hostmetrics

import (
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
)

func TestHostValuesAggregatesEntitiesAndUnavailableRates(t *testing.T) {
	values := hostValues(&hostmetricpb.HostSnapshot{
		Cpu:         &hostmetricpb.CpuMetric{UsageAvailable: false, UsagePercent: 99},
		Memory:      &hostmetricpb.MemoryMetric{UsagePercent: 50},
		Filesystems: []*hostmetricpb.FilesystemMetric{{UsagePercent: 40}, {UsagePercent: 90}},
		Disks:       []*hostmetricpb.DiskMetric{{RateAvailable: true, UtilizationPercent: 20}, {RateAvailable: true, UtilizationPercent: 70}},
		Networks:    []*hostmetricpb.NetworkMetric{{RateAvailable: true, ReceiveErrorsTotal: 2}, {RateAvailable: false, ReceiveErrorsTotal: 100}},
	})
	if values[HostMetricCPU].available {
		t.Fatal("unavailable CPU was treated as available")
	}
	if values[HostMetricFilesystemUsage].value != 90 || values[HostMetricDiskUtilization].value != 70 || values[HostMetricNetworkErrors].value != 2 {
		t.Fatalf("aggregated values=%v", values)
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

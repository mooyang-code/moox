package bootstrap

import (
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestReporterExpectationWithNoProbeUsesStableDeploymentIdentity(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	messages, err := store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	query := monmetrics.NewQueryService(messages, nil)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	for i := range catalog.Components {
		if catalog.Components[i].ID == "factor-mgr" {
			catalog.Components[i].Health = servicecatalog.Health{Kind: "none"}
		}
	}
	now := time.Now().UTC()
	snapshot := domain.TopologySnapshot{Catalog: catalog, ObservedAt: now,
		Hosts:      []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}},
		Placements: []domain.TopologyPlacement{{HostID: "control", ComponentID: "factor-mgr", Status: "enabled"}},
	}
	repos := manager.Repositories()
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Topology: repos.Topology, Now: func() time.Time { return now }}, repos, nil)
	require.NoError(t, run(t.Context()))
	const id = "reporter:control:factor-mgr"
	results, err := repos.Results.Recent(t.Context(), "mooxsys", id, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Success, "registration creates a never-reported expectation without a probe")
	require.NoError(t, messages.TouchService(t.Context(), &eventpb.EventMessage{OccurredAt: timestamppb.New(now)}, &metricspb.MetricReport{ServiceName: "factor-mgr", NodeId: "control", InstanceId: "factor-mgr@control", BootId: "boot-1"}))
	require.NoError(t, run(t.Context()))
	results, err = repos.Results.Recent(t.Context(), "mooxsys", id, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.True(t, results[0].Success, "the first report resolves the same check identity")
	checks, err := repos.Checks.List(t.Context(), store.ListChecksOptions{Source: domain.CheckSourceObservability, Page: store.Page{PageSize: 10}})
	require.NoError(t, err)
	require.Len(t, checks, 1)
	snapshot.Placements[0].Status = "disabled"
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	check, err := repos.Checks.Get(t.Context(), "mooxsys", id)
	require.NoError(t, err)
	require.False(t, check.Enabled)
	results, err = repos.Results.Recent(t.Context(), "mooxsys", id, 10)
	require.NoError(t, err)
	require.Len(t, results, 2, "disable does not fabricate a successful report")
	snapshot.Placements[0].Status = "enabled"
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	check, err = repos.Checks.Get(t.Context(), "mooxsys", id)
	require.NoError(t, err)
	require.True(t, check.Enabled, "reenabled deployments restore reporter checks")
	results, err = repos.Results.Recent(t.Context(), "mooxsys", id, 10)
	require.NoError(t, err)
	require.Len(t, results, 3)
	snapshot.Placements = nil
	_, err = repos.Topology.Reconcile(t.Context(), snapshot, nil)
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	check, err = repos.Checks.Get(t.Context(), "mooxsys", id)
	require.NoError(t, err)
	require.False(t, check.Enabled, "deleting a placement retires its reporter check")

}

func TestMetricsRouteAcceptsUnregisteredReportsWithoutBypassingValidation(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	messages, err := store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	query := monmetrics.NewQueryService(messages, nil)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	now := time.Now().UTC()
	_, err = manager.Repositories().Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now}, nil)
	require.NoError(t, err)
	route := metricsObservabilityRoute(messages, nil, nil, true)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	raw := []byte("# TYPE moox_collector_kline_resample_claims_total gauge\nmoox_collector_kline_resample_claims_total 1\n")
	sum := sha256.Sum256(raw)
	report := &metricspb.MetricReport{
		ServiceName: "collector", InstanceId: "collector@control", NodeId: "control", BootId: "boot-1",
		Snapshot: &metricspb.MetricSnapshot{SchemaVersion: 1, Format: metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT,
			Data: raw, DataSha256: sum[:], SampleCount: 1},
	}
	encode := func(id string, observed time.Time, report *metricspb.MetricReport) events.EncodedEvent {
		t.Helper()
		encoded, err := registry.Encode(events.ObservabilityMetricsSnapshotReported, report, events.PublishOptions{EventID: id, SpaceID: "mooxsys", SubjectID: report.ServiceName + "/" + report.InstanceId, OccurredAt: observed})
		require.NoError(t, err)
		encoded.Payload, err = proto.Marshal(encoded.Message)
		require.NoError(t, err)
		return encoded
	}
	ingest := func(encoded events.EncodedEvent) error {
		t.Helper()
		message, payload, err := events.DecodeRaw(registry, encoded.Payload, encoded.Subject, encoded.Message.GetEventId(), events.ContentType)
		require.NoError(t, err)
		return route(t.Context(), message, payload.(*metricspb.MetricReport))
	}
	encoded := encode("unregistered", now, report)
	require.NoError(t, ingest(encoded))
	require.NoError(t, ingest(encoded))
	series, err := messages.ListSeries(t.Context(), "collector", "moox_collector_kline_resample_claims_total", 10)
	require.NoError(t, err)
	require.Len(t, series, 1, "redelivery stays idempotent")
	got, err := (monitorobservability.Builder{Metrics: query, Topology: manager.Repositories().Topology, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, got.Unregistered, 1)
	require.Equal(t, "collector", got.Unregistered[0].ServiceName)
	_, _, err = events.DecodeRaw(registry, encoded.Payload, encoded.Subject+".wrong", encoded.Message.GetEventId(), events.ContentType)
	require.ErrorContains(t, err, "subject mismatch")
	_, _, err = events.DecodeRaw(registry, encoded.Payload, encoded.Subject, "wrong-id", events.ContentType)
	require.ErrorContains(t, err, "does not match")
	invalid := proto.Clone(report).(*metricspb.MetricReport)
	invalid.Snapshot.Data = []byte("corrupted")
	require.ErrorContains(t, ingest(encode("invalid-snapshot", now, invalid)), "checksum mismatch")
	duplicate, err := messages.IsDuplicate(t.Context(), "invalid-snapshot")
	require.NoError(t, err)
	require.False(t, duplicate)
	old := proto.Clone(report).(*metricspb.MetricReport)
	old.ServiceName, old.InstanceId = "archive", "archive@control"
	require.NoError(t, ingest(encode("old-report", now.Add(-time.Hour), old)))
	rows, _, err := query.Catalog().ListServicesAt(t.Context(), "", 0, 10, now)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for _, row := range rows {
		if row.ServiceName == "archive" {
			require.True(t, row.IsStale, "committing delayed history cannot revive an old reporter")
			require.WithinDuration(t, now.Add(-time.Hour), row.LastSeenAt, time.Millisecond)
		}
	}
}

func TestMetricsRouteCountsFilteredSamplesAndRejectReasons(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	messages, err := store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	route := metricsObservabilityRoute(messages, nil, &Runtime{}, true)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)

	ingest := func(id, space string, report *metricspb.MetricReport) error {
		t.Helper()
		encoded, err := registry.Encode(events.ObservabilityMetricsSnapshotReported, report, events.PublishOptions{EventID: id, SpaceID: space, SubjectID: report.ServiceName + "/" + report.InstanceId, OccurredAt: time.Now().UTC()})
		require.NoError(t, err)
		encoded.Payload, err = proto.Marshal(encoded.Message)
		require.NoError(t, err)
		message, payload, err := events.DecodeRaw(registry, encoded.Payload, encoded.Subject, encoded.Message.GetEventId(), events.ContentType)
		require.NoError(t, err)
		return route(t.Context(), message, payload.(*metricspb.MetricReport))
	}
	snapshot := func(raw string) *metricspb.MetricSnapshot {
		sum := sha256.Sum256([]byte(raw))
		return &metricspb.MetricSnapshot{SchemaVersion: 1, Format: metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT,
			Data: []byte(raw), DataSha256: sum[:], SampleCount: 2}
	}
	// 一个在白名单里、一个不在：后者按白名单过滤并计数。
	raw := "# TYPE moox_collector_kline_resample_claims_total gauge\nmoox_collector_kline_resample_claims_total 1\n# TYPE not_a_health_metric gauge\nnot_a_health_metric 5\n"
	before := counterValue(t, "moox_monitor_metrics_samples_filtered_total", map[string]string{"service": "filter-svc"})
	good := &metricspb.MetricReport{ServiceName: "filter-svc", InstanceId: "filter-svc@node", NodeId: "node", BootId: "boot", Snapshot: snapshot(raw)}
	require.NoError(t, ingest("filter-1", "mooxsys", good))
	require.Equal(t, before+1, counterValue(t, "moox_monitor_metrics_samples_filtered_total", map[string]string{"service": "filter-svc"}))
	// 同一事件重复投递：已入库的快照不再累加过滤数。
	require.NoError(t, ingest("filter-1", "mooxsys", good))
	require.Equal(t, before+1, counterValue(t, "moox_monitor_metrics_samples_filtered_total", map[string]string{"service": "filter-svc"}))

	// 校验和不对：按 checksum 拒收，并记在该服务名下。
	bad := &metricspb.MetricReport{ServiceName: "reject-svc", InstanceId: "reject-svc@node", NodeId: "node", BootId: "boot", Snapshot: snapshot(raw)}
	bad.Snapshot.DataSha256 = make([]byte, 32)
	require.Error(t, ingest("reject-1", "mooxsys", bad))
	// 空间不对：按 space 拒收。
	require.Error(t, ingest("reject-2", "crypto", &metricspb.MetricReport{ServiceName: "space-svc", InstanceId: "space-svc@node", NodeId: "node", BootId: "boot", Snapshot: snapshot(raw)}))

	reasons := map[string]string{}
	for _, item := range messages.IngestRejects() {
		reasons[item.Service] = item.LastReason
	}
	require.Equal(t, map[string]string{"reject-svc": monmetrics.RejectChecksum, "space-svc": monmetrics.RejectSpace}, reasons)
}

// counterValue 从默认注册表读出带指定标签的计数器当前值；指标还没出现过时为 0。
func counterValue(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			matched := 0
			for _, pair := range metric.GetLabel() {
				if want, ok := labels[pair.GetName()]; ok && want == pair.GetValue() {
					matched++
				}
			}
			if matched == len(labels) {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

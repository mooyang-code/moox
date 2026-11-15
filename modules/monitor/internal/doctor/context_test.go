package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/deployment"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

type deploymentSourceStub struct {
	rows []*adminpb.ComponentDeployment
	err  error
}

func TestBuilderKeepsEveryHostDeploymentAndDoesNotInferMissingDefaults(t *testing.T) {
	builder := Builder{Deployments: deploymentSourceStub{rows: []*adminpb.ComponentDeployment{
		{ComponentId: "access", HostId: "node-a", Status: "enabled"},
		{ComponentId: "access", HostId: "node-b", Status: "disabled"},
	}}}
	got, err := builder.Build(t.Context(), "", []string{"access", "collector"}, nil)
	require.NoError(t, err)
	require.Len(t, got.ExpectedComponents, 3)
	byHost := map[string]ExpectedComponent{}
	for _, component := range got.ExpectedComponents {
		byHost[component.NodeID+":"+component.ComponentID] = component
	}
	require.True(t, byHost["node-a:access"].Expected)
	require.False(t, byHost["node-b:access"].Expected)
	require.False(t, byHost[":collector"].Expected, "catalog defaults must not invent a deployment")
}

// 健康观察项取最近一次上报里的就绪判断：未就绪为 DOWN 并带原因，上报超过两个间隔为过期，没有上报记为缺失。
func TestBuilderHealthReadsReportedReadiness(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	messages, err := store.WithDatabase(mgr, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	_, err = messages.CommitIngest(t.Context(), &eventpb.EventMessage{EventId: "m1", OccurredAt: timestamppb.New(now.Add(-3 * time.Minute))},
		&metricspb.MetricReport{ServiceName: "monitor", InstanceId: "monitor@node-a", NodeId: "node-a", BootId: "boot-a", Ready: proto.Bool(false), NotReadyReason: "观测输入未连接"}, nil)
	require.NoError(t, err)
	builder := Builder{
		Deployments: deploymentSourceStub{rows: []*adminpb.ComponentDeployment{{ComponentId: "monitor", HostId: "node-a", Status: "enabled"}, {ComponentId: "eventbus", HostId: "node-a", Status: "enabled"}}},
		Metrics:     monmetrics.NewQueryService(messages, nil), HealthChecks: report.BuiltInModuleHealthChecks(), Now: func() time.Time { return now },
	}
	got, err := builder.Build(context.Background(), "node-a", []string{"monitor", "eventbus"}, nil)
	require.NoError(t, err)
	require.Len(t, got.HealthObservations, 1)
	require.Equal(t, "DOWN", got.HealthObservations[0].Status)
	require.Contains(t, got.HealthObservations[0].Summary, "观测输入未连接")
	require.True(t, got.HealthObservations[0].Stale)
	require.Equal(t, 30, got.HealthObservations[0].IntervalSeconds)
	missing := false
	for _, observation := range got.MissingObservations {
		missing = missing || (observation.Kind == "health" && observation.ComponentID == "eventbus")
	}
	require.True(t, missing, "没有上报的组件记为缺失")
}

func (s deploymentSourceStub) Snapshot(context.Context) (deployment.Snapshot, error) {
	catalog, _ := servicecatalog.LoadEmbedded()
	hosts := map[string]bool{}
	snapshot := deployment.Snapshot{Catalog: catalog, Deployments: s.rows}
	for _, row := range s.rows {
		if !hosts[row.GetHostId()] {
			hosts[row.GetHostId()] = true
			snapshot.Hosts = append(snapshot.Hosts, &adminpb.DeploymentHost{HostId: row.GetHostId(), Address: row.GetHostId() + ".example.test", Status: "enabled"})
		}
	}
	return snapshot, s.err
}

func TestBuilderMarksDisabledAsNotExpectedAndStorageDeferred(t *testing.T) {
	builder := Builder{
		Deployments: deploymentSourceStub{rows: []*adminpb.ComponentDeployment{
			{ComponentId: "factor-mgr", HostId: "node-a", Status: "disabled"},
			{ComponentId: "storage-primary", HostId: "node-a", Status: "enabled"},
		}},
		HealthChecks: []report.ModuleHealthCheck{{ID: "factor-calculation", Module: "factor", MaxLag: time.Minute, Enabled: true}},
		Now:          func() time.Time { return time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC) },
	}
	got, err := builder.Build(context.Background(), "node-a", []string{"factor-mgr", "storage-primary"}, []string{"factor-calculation"})
	require.NoError(t, err)
	require.Len(t, got.ExpectedComponents, 2)
	byID := map[string]ExpectedComponent{}
	for _, component := range got.ExpectedComponents {
		byID[component.ComponentID] = component
	}
	require.False(t, byID["factor-mgr"].Expected)
	require.Equal(t, "deferred", byID["storage-primary"].FunctionalObservability)
	require.Empty(t, got.Watermarks)
}

func TestBuilderRejectsUnknownSelections(t *testing.T) {
	builder := Builder{Deployments: deploymentSourceStub{}, HealthChecks: report.BuiltInModuleHealthChecks()}
	_, err := builder.Build(context.Background(), "", []string{"not-a-component"}, nil)
	require.ErrorContains(t, err, "unknown component_id")
	_, err = builder.Build(context.Background(), "", nil, []string{"not-a-health-check"})
	require.ErrorContains(t, err, "unknown health_check_id")
}

func TestBuilderReadsCanonicalModuleMetricNames(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	messageStore, err := store.WithDatabase(mgr, func(db *gorm.DB) *monmetrics.MetricMessageStore {
		return monmetrics.NewMetricMessageStore(db)
	})
	require.NoError(t, err)
	labels := `{"health_check":"monitor-metrics","stage":"ingest"}`
	samples := []monmetrics.Sample{
		{SeriesID: "success", ServiceName: "monitor", InstanceID: "monitor@node-a", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricLastSuccess), MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Unix()), ObservedAt: now, Interval: 30 * time.Second, MessageID: "message"},
		{SeriesID: "input", ServiceName: "monitor", InstanceID: "monitor@node-a", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricInputWatermark), MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Add(-time.Second).Unix()), ObservedAt: now, Interval: 30 * time.Second, MessageID: "message"},
		{SeriesID: "output", ServiceName: "monitor", InstanceID: "monitor@node-a", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricBusinessWatermark), MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Unix()), ObservedAt: now, Interval: 30 * time.Second, MessageID: "message"},
	}
	for i := 0; i < MaxSeries+1; i++ {
		samples = append(samples, monmetrics.Sample{
			SeriesID: fmt.Sprintf("dataset-%03d", i), ServiceName: "monitor",
			InstanceID: "monitor@node-a", MetricName: "moox_monitor_dataset_last_success_timestamp_seconds",
			MetricType: "gauge", LabelsJSON: fmt.Sprintf(`{"dataset_id":"dataset-%03d","freq":"1m","space_id":"crypto"}`, i),
			Value: float64(now.Unix()), ObservedAt: now, Interval: 30 * time.Second, MessageID: "message",
		})
	}
	_, err = messageStore.CommitIngest(
		context.Background(),
		&eventpb.EventMessage{EventId: "message", OccurredAt: timestamppb.New(now)},
		&metricspb.MetricReport{ServiceName: "monitor", InstanceId: "monitor@node-a", NodeId: "node-a", BootId: "boot-a"},
		samples,
	)
	require.NoError(t, err)
	otherLabels := `{"health_check":"monitor-metrics","stage":"ingest"}`
	otherSamples := []monmetrics.Sample{
		{SeriesID: "other-success", ServiceName: "monitor", InstanceID: "monitor@node-b", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricLastSuccess), MetricType: "gauge", LabelsJSON: otherLabels, Value: float64(now.Add(-time.Hour).Unix()), ObservedAt: now.Add(-time.Hour), Interval: 30 * time.Second, MessageID: "other-message"},
		{SeriesID: "other-input", ServiceName: "monitor", InstanceID: "monitor@node-b", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricInputWatermark), MetricType: "gauge", LabelsJSON: otherLabels, Value: float64(now.Add(-time.Hour).Unix()), ObservedAt: now.Add(-time.Hour), Interval: 30 * time.Second, MessageID: "other-message"},
		{SeriesID: "other-output", ServiceName: "monitor", InstanceID: "monitor@node-b", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricBusinessWatermark), MetricType: "gauge", LabelsJSON: otherLabels, Value: float64(now.Add(-time.Hour).Unix()), ObservedAt: now.Add(-time.Hour), Interval: 30 * time.Second, MessageID: "other-message"},
	}
	_, err = messageStore.CommitIngest(
		context.Background(),
		&eventpb.EventMessage{EventId: "other-message", OccurredAt: timestamppb.New(now.Add(-time.Hour))},
		&metricspb.MetricReport{ServiceName: "monitor", InstanceId: "monitor@node-b", NodeId: "node-b", BootId: "boot-b"},
		otherSamples,
	)
	require.NoError(t, err)
	healthChecks := []report.ModuleHealthCheck{{
		ID: "monitor-metrics", Module: "monitor", Enabled: true, CheckWatermark: true,
	}}
	got, err := (Builder{
		Deployments: deploymentSourceStub{rows: []*adminpb.ComponentDeployment{{
			ComponentId: "monitor", HostId: "node-a", Status: "enabled",
		}}},
		Metrics:      messageQuery(messageStore),
		HealthChecks: healthChecks,
		Now:          func() time.Time { return now },
	}).Build(context.Background(), "node-a", []string{"monitor"}, []string{"monitor-metrics"})
	require.NoError(t, err)
	require.Len(t, got.ModuleObservations, 3)
	for _, observation := range got.ModuleObservations {
		require.Equal(t, "monitor@node-a", observation.InstanceID)
	}
	require.Len(t, got.Watermarks, 1)
	require.Equal(t, "monitor", got.Watermarks[0].Module)
	require.Equal(t, "ingest", got.Watermarks[0].Stage)
	require.Equal(t, "monitor-metrics", got.Watermarks[0].HealthCheckID)
}

func messageQuery(messageStore *monmetrics.MetricMessageStore) *monmetrics.QueryService {
	return monmetrics.NewQueryService(messageStore, nil)
}

func TestBuilderReportsIngestDropsAndRejectsWithoutBreakingBounds(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	messages, err := store.WithDatabase(mgr, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	messages.SetSeriesLimits(monmetrics.SeriesLimits{MaxNewPerHour: 1, MaxNewPerDay: 100})
	now := time.Now().UTC()
	report := &metricspb.MetricReport{ServiceName: "noisy", InstanceId: "noisy@node", BootId: "boot"}
	_, err = messages.CommitIngest(t.Context(), &eventpb.EventMessage{EventId: "e1"}, report, []monmetrics.Sample{
		{SeriesID: "s1", ServiceName: "noisy", InstanceID: "noisy@node", MetricName: "m_keep", MetricType: "gauge", ObservedAt: now, MessageID: "e1"},
		{SeriesID: "s2", ServiceName: "noisy", InstanceID: "noisy@node", MetricName: "m_dropped", MetricType: "gauge", ObservedAt: now, MessageID: "e1"},
	})
	require.NoError(t, err)
	messages.RecordIngestRejected(nil, "broken", monmetrics.RejectChecksum)

	builder := Builder{Metrics: monmetrics.NewQueryService(messages, nil), Now: func() time.Time { return now }}
	got, err := builder.Build(t.Context(), "", nil, nil)
	require.NoError(t, err)
	byKind := map[string]Observation{}
	for _, item := range got.ModuleObservations {
		if item.Kind == "metrics_ingest" {
			byKind[item.Summary] = item
		}
	}
	dropped := byKind["metrics_ingest_series_dropped"]
	require.Equal(t, "noisy", dropped.ServiceName)
	require.EqualValues(t, 1, dropped.Value)
	require.Contains(t, dropped.DetailsJSON, `"last_metric":"m_dropped"`)
	rejected := byKind["metrics_ingest_snapshot_rejected"]
	require.Equal(t, "broken", rejected.ServiceName)
	require.Contains(t, rejected.DetailsJSON, `"last_reason":"checksum"`)
}

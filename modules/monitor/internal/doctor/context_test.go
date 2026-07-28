package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

type deploymentSourceStub struct {
	rows []*adminpb.ServiceDeployment
	err  error
}

func TestBuilderHealthUsesPerComponentFreshnessAndConsecutiveFailures(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	repos := mgr.Repositories()
	require.NoError(t, repos.Checks.Create(context.Background(), &domain.Check{SpaceID: "", CheckID: "sysdeploy:node-a:moox_monitor", IntervalSeconds: 30, Enabled: true}))
	for i := 0; i < 3; i++ {
		require.NoError(t, repos.Results.Insert(context.Background(), &domain.CheckResult{ResultID: time.Now().Add(time.Duration(i) * time.Nanosecond).String(), CheckID: "sysdeploy:node-a:moox_monitor", Status: domain.CheckStatusDegraded, Success: false, BodyExcerpt: `{"service":"moox_monitor","instance_id":"moox_monitor@node-a","node_id":"node-a","boot_id":"boot-a"}`, CheckedAt: now.Add(-time.Duration(i+3) * time.Minute)}))
	}
	builder := Builder{Deployments: deploymentSourceStub{rows: []*adminpb.ServiceDeployment{{ServiceName: "moox_monitor", NodeId: "node-a", Status: "active"}}}, Checks: repos.Checks, Results: repos.Results, Pipelines: report.PipelineConfig{Version: 1}, Now: func() time.Time { return now }}
	got, err := builder.Build(context.Background(), "node-a", []string{"moox_monitor"}, nil)
	require.NoError(t, err)
	require.Len(t, got.HealthObservations, 1)
	require.Equal(t, "DOWN", got.HealthObservations[0].Status)
	require.True(t, got.HealthObservations[0].Stale)
	require.Equal(t, 30, got.HealthObservations[0].IntervalSeconds)
}

func (s deploymentSourceStub) DesiredDeployments(context.Context) ([]*adminpb.ServiceDeployment, error) {
	return s.rows, s.err
}

func TestBuilderMarksDisabledAsNotExpectedAndStorageDeferred(t *testing.T) {
	builder := Builder{
		Deployments: deploymentSourceStub{rows: []*adminpb.ServiceDeployment{
			{ServiceName: "moox_factor", NodeId: "node-a", Status: "disabled"},
			{ServiceName: "storage-primary", NodeId: "node-a", Status: "active"},
		}},
		Pipelines: report.PipelineConfig{Version: 1, Pipelines: []report.Pipeline{{ID: "factor-calculation", Module: "factor", SpaceID: "default", InputDataset: "bars", OutputDataset: "factors", LagTolerance: time.Minute, Enabled: true}}},
		Now:       func() time.Time { return time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC) },
	}
	got, err := builder.Build(context.Background(), "node-a", []string{"moox_factor", "storage_primary"}, []string{"factor-calculation"})
	require.NoError(t, err)
	require.Len(t, got.ExpectedComponents, 2)
	byID := map[string]ExpectedComponent{}
	for _, component := range got.ExpectedComponents {
		byID[component.ComponentID] = component
	}
	require.False(t, byID["moox_factor"].Expected)
	require.Equal(t, "deferred", byID["storage_primary"].FunctionalObservability)
	require.Empty(t, got.Watermarks)
}

func TestBuilderDoesNotRequireReporterIdentityForNotApplicableComponent(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	repos := mgr.Repositories()
	require.NoError(t, repos.Checks.Create(context.Background(), &domain.Check{SpaceID: "", CheckID: "sysdeploy:node-a:eventbus", IntervalSeconds: 30, Enabled: true}))
	require.NoError(t, repos.Results.Insert(context.Background(), &domain.CheckResult{ResultID: "eventbus-result", CheckID: "sysdeploy:node-a:eventbus", Status: domain.CheckStatusOK, Success: true, BodyExcerpt: `{}`, CheckedAt: now}))
	builder := Builder{
		Deployments: deploymentSourceStub{rows: []*adminpb.ServiceDeployment{{ServiceName: "eventbus", NodeId: "node-a", Status: "active"}}},
		Checks:      repos.Checks,
		Results:     repos.Results,
		Now:         func() time.Time { return now },
	}
	got, err := builder.Build(context.Background(), "node-a", []string{"eventbus"}, nil)
	require.NoError(t, err)
	require.Len(t, got.HealthObservations, 1)
	require.False(t, got.HealthObservations[0].Conflict)
}

func TestBuilderRejectsUnknownSelections(t *testing.T) {
	builder := Builder{Deployments: deploymentSourceStub{}, Pipelines: report.PipelineConfig{Version: 1}}
	_, err := builder.Build(context.Background(), "", []string{"not-a-component"}, nil)
	require.ErrorContains(t, err, "unknown component_id")
	_, err = builder.Build(context.Background(), "", nil, []string{"not-a-pipeline"})
	require.ErrorContains(t, err, "unknown pipeline_id")
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
	labels := `{"pipeline":"monitor-metrics","stage":"ingest"}`
	samples := []monmetrics.Sample{
		{SeriesID: "success", ServiceName: "moox_monitor", InstanceID: "moox_monitor@node-a", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricLastSuccess), MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Unix()), ObservedAt: now, Interval: 30 * time.Second, MessageID: "message"},
		{SeriesID: "input", ServiceName: "moox_monitor", InstanceID: "moox_monitor@node-a", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricInputWatermark), MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Add(-time.Second).Unix()), ObservedAt: now, Interval: 30 * time.Second, MessageID: "message"},
		{SeriesID: "output", ServiceName: "moox_monitor", InstanceID: "moox_monitor@node-a", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricBusinessWatermark), MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Unix()), ObservedAt: now, Interval: 30 * time.Second, MessageID: "message"},
	}
	for i := 0; i < MaxSeries+1; i++ {
		samples = append(samples, monmetrics.Sample{
			SeriesID: fmt.Sprintf("dataset-%03d", i), ServiceName: "moox_monitor",
			InstanceID: "moox_monitor@node-a", MetricName: "moox_monitor_dataset_last_success_timestamp_seconds",
			MetricType: "gauge", LabelsJSON: fmt.Sprintf(`{"dataset_id":"dataset-%03d","freq":"1m","space_id":"crypto"}`, i),
			Value: float64(now.Unix()), ObservedAt: now, Interval: 30 * time.Second, MessageID: "message",
		})
	}
	_, err = messageStore.CommitIngest(
		context.Background(),
		&eventpb.EventMessage{EventId: "message", OccurredAt: timestamppb.New(now)},
		&metricspb.MetricReport{ServiceName: "moox_monitor", InstanceId: "moox_monitor@node-a", NodeId: "node-a", BootId: "boot-a"},
		samples,
	)
	require.NoError(t, err)
	otherLabels := `{"pipeline":"monitor-metrics","stage":"ingest"}`
	otherSamples := []monmetrics.Sample{
		{SeriesID: "other-success", ServiceName: "moox_monitor", InstanceID: "moox_monitor@node-b", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricLastSuccess), MetricType: "gauge", LabelsJSON: otherLabels, Value: float64(now.Add(-time.Hour).Unix()), ObservedAt: now.Add(-time.Hour), Interval: 30 * time.Second, MessageID: "other-message"},
		{SeriesID: "other-input", ServiceName: "moox_monitor", InstanceID: "moox_monitor@node-b", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricInputWatermark), MetricType: "gauge", LabelsJSON: otherLabels, Value: float64(now.Add(-time.Hour).Unix()), ObservedAt: now.Add(-time.Hour), Interval: 30 * time.Second, MessageID: "other-message"},
		{SeriesID: "other-output", ServiceName: "moox_monitor", InstanceID: "moox_monitor@node-b", MetricName: report.ModuleMetricName("monitor", report.ModuleMetricBusinessWatermark), MetricType: "gauge", LabelsJSON: otherLabels, Value: float64(now.Add(-time.Hour).Unix()), ObservedAt: now.Add(-time.Hour), Interval: 30 * time.Second, MessageID: "other-message"},
	}
	_, err = messageStore.CommitIngest(
		context.Background(),
		&eventpb.EventMessage{EventId: "other-message", OccurredAt: timestamppb.New(now.Add(-time.Hour))},
		&metricspb.MetricReport{ServiceName: "moox_monitor", InstanceId: "moox_monitor@node-b", NodeId: "node-b", BootId: "boot-b"},
		otherSamples,
	)
	require.NoError(t, err)
	pipelines := report.PipelineConfig{Pipelines: []report.Pipeline{{
		ID: "monitor-metrics", Module: "monitor", Enabled: true, WatermarkMonitoring: true,
	}}}
	got, err := (Builder{
		Deployments: deploymentSourceStub{rows: []*adminpb.ServiceDeployment{{
			ServiceName: "moox_monitor", NodeId: "node-a", Status: "active",
		}}},
		Metrics:   messageQuery(messageStore),
		Pipelines: pipelines,
		Now:       func() time.Time { return now },
	}).Build(context.Background(), "node-a", []string{"moox_monitor"}, []string{"monitor-metrics"})
	require.NoError(t, err)
	require.Len(t, got.ModuleObservations, 3)
	for _, observation := range got.ModuleObservations {
		require.Equal(t, "moox_monitor@node-a", observation.InstanceID)
	}
	require.Len(t, got.Watermarks, 1)
	require.Equal(t, "monitor", got.Watermarks[0].Module)
	require.Equal(t, "ingest", got.Watermarks[0].Stage)
	require.Equal(t, "monitor-metrics", got.Watermarks[0].Pipeline)
}

func messageQuery(messageStore *monmetrics.MetricMessageStore) *monmetrics.QueryService {
	return monmetrics.NewQueryService(messageStore, nil)
}

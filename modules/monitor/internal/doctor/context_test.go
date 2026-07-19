package doctor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, repos.Checks.Create(context.Background(), &domain.Check{SpaceID: "", CheckID: "moox_monitor", IntervalSeconds: 30, Enabled: true}))
	for i := 0; i < 3; i++ {
		require.NoError(t, repos.Results.Insert(context.Background(), &domain.CheckResult{ResultID: time.Now().Add(time.Duration(i) * time.Nanosecond).String(), CheckID: "moox_monitor", Status: domain.CheckStatusDegraded, Success: false, BodyExcerpt: `{"service":"moox_monitor","instance_id":"moox_monitor@node-a","boot_id":"boot-a"}`, CheckedAt: now.Add(-time.Duration(i+3) * time.Minute)}))
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

func TestBuilderRejectsUnknownSelections(t *testing.T) {
	builder := Builder{Deployments: deploymentSourceStub{}, Pipelines: report.PipelineConfig{Version: 1}}
	_, err := builder.Build(context.Background(), "", []string{"not-a-component"}, nil)
	require.ErrorContains(t, err, "unknown component_id")
	_, err = builder.Build(context.Background(), "", nil, []string{"not-a-pipeline"})
	require.ErrorContains(t, err, "unknown pipeline_id")
}

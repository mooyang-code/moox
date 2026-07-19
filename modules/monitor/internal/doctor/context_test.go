package doctor

import (
	"context"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
)

type deploymentSourceStub struct {
	rows []*adminpb.ServiceDeployment
	err  error
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

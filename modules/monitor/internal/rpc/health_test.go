package rpc

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestHealthOverviewRPCV2CarriesIdentityIndependentSignalsAndStatusSince(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.SQL()))
	repos := db.Repositories()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	err = repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now, Hosts: []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}}, Deployments: []domain.TopologyDeployment{{HostID: "control", ComponentID: "web-host", Status: "enabled"}}})
	require.NoError(t, err)
	require.NoError(t, repos.ComponentHealth.Reconcile(t.Context(), []domain.ComponentHealthState{{HostID: "control", ComponentID: "web-host", Status: "unknown"}}, now.Add(-time.Minute)))
	facts := &observability.Builder{Topology: repos.Topology, Now: func() time.Time { return now }}
	service := New(repos, Options{HealthView: &healthview.Builder{Facts: facts, ComponentHealth: repos.ComponentHealth, Now: func() time.Time { return now }}})
	rsp, err := service.GetHealthOverview(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.Overview.Components, 1)
	component := rsp.Overview.Components[0]
	require.Equal(t, "control", component.HostId)
	require.Equal(t, "web-host", component.ComponentId)
	require.Equal(t, now.Add(-time.Minute).Format(time.RFC3339Nano), component.StatusSince)
	require.Equal(t, "unknown", component.Reporter.Status, "从未上报")
	require.Empty(t, component.Reporter.CheckedAt)
	for _, encoding := range []string{"pb", "json"} {
		t.Run(encoding, func(t *testing.T) {
			decoded := &monitorpb.GetHealthOverviewRsp{}
			if encoding == "pb" {
				encoded, err := proto.Marshal(rsp)
				require.NoError(t, err)
				require.NoError(t, proto.Unmarshal(encoded, decoded))
			} else {
				encoded, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(rsp)
				require.NoError(t, err)
				require.Contains(t, string(encoded), "components")
				require.NotContains(t, string(encoded), "service_items")
				require.NoError(t, protojson.Unmarshal(encoded, decoded))
			}
			require.True(t, proto.Equal(rsp, decoded))
		})
	}
}

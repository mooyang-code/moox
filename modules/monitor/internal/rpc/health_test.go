package rpc

import (
	"fmt"
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
	_, err = repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now, Hosts: []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}}, Placements: []domain.TopologyPlacement{{HostID: "control", ComponentID: "web-host", Status: "enabled"}}}, nil)
	require.NoError(t, err)
	require.NoError(t, repos.Checks.Create(t.Context(), &domain.Check{CheckID: "placement:control:web-host", URL: "http://127.0.0.1:11002/healthz", Kind: domain.CheckKindHTTP, Enabled: true, Headers: `{"Authorization":"test-only-header"}`}))
	for i := 1; i < 12; i++ {
		require.NoError(t, repos.Results.Insert(t.Context(), &domain.CheckResult{ResultID: fmt.Sprintf("history-%d", i), CheckID: "placement:control:web-host", Status: "ok", Success: true, CheckedAt: now.Add(-time.Duration(i) * time.Minute)}))
	}
	require.NoError(t, repos.Results.Insert(t.Context(), &domain.CheckResult{ResultID: "failed", CheckID: "placement:control:web-host", Status: "down", ErrorMessage: "页面服务探测失败", RawError: "dial tcp: refused\n原始错误", CheckedAt: now}))
	require.NoError(t, repos.ComponentHealth.Reconcile(t.Context(), []domain.ComponentHealthState{{HostID: "control", ComponentID: "web-host", Status: "down"}}, now.Add(-time.Minute)))
	facts := &observability.Builder{Checks: repos.Checks, Topology: repos.Topology, Results: repos.Results, Now: func() time.Time { return now }}
	service := New(repos, Options{HealthView: &healthview.Builder{Facts: facts, ComponentHealth: repos.ComponentHealth, Now: func() time.Time { return now }}})
	rsp, err := service.GetHealthOverview(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.Overview.Components, 1)
	component := rsp.Overview.Components[0]
	require.Equal(t, "http://127.0.0.1:11002/healthz", component.ProbeUrl)
	require.Len(t, component.RecentProbes, 10)
	require.Equal(t, "control", component.HostId)
	require.Equal(t, "web-host", component.ComponentId)
	require.Equal(t, now.Add(-time.Minute).Format(time.RFC3339Nano), component.StatusSince)
	require.Equal(t, "dial tcp: refused\n原始错误", component.Probe.RawError)
	require.Empty(t, component.Reporter.CheckedAt, "a probe cannot manufacture a reporter timestamp")
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
				require.NotContains(t, string(encoded), "test-only-header")
				require.NoError(t, protojson.Unmarshal(encoded, decoded))
			}
			require.True(t, proto.Equal(rsp, decoded))
		})
	}
}

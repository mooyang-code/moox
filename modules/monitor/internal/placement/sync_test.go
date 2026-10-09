package placement

import (
	"context"
	"path/filepath"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

func testRepositories(t *testing.T) *store.Repositories {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	return mgr.Repositories()
}

func productionHosts() []*adminpb.DeployHost {
	return []*adminpb.DeployHost{
		{HostId: "control", Address: "106.53.107.122", Status: StatusEnabled},
		{HostId: "storage", Address: "146.56.196.204", PrivateAddress: "10.206.0.5", Status: StatusEnabled},
		{HostId: "compute-1", Address: "43.132.204.177", Status: StatusEnabled},
	}
}

func placementRow(host, component, status string) *adminpb.DeployPlacement {
	return &adminpb.DeployPlacement{HostId: host, ComponentId: component, Status: status}
}

func checkState(t *testing.T, repos *store.Repositories, host, component string) (*domain.Check, bool) {
	t.Helper()
	check, err := repos.Checks.Get(context.Background(), "", CheckID(host, component))
	if err != nil {
		return nil, false
	}
	return check, true
}

func TestSyncerAddsDisablesAndDeletesChecksWithPlacements(t *testing.T) {
	repos := testRepositories(t)
	syncer := NewSyncer(repos.Checks, nil, nil)
	ctx := context.Background()
	steps := []struct {
		name       string
		hosts      []*adminpb.DeployHost
		placements []*adminpb.DeployPlacement
		want       map[string]bool // 检查 ID → 是否启用；不在表里的检查应当不存在
	}{
		{
			name:  "部署启用时增加检查",
			hosts: productionHosts(),
			placements: []*adminpb.DeployPlacement{
				placementRow("control", "monitor", StatusEnabled),
				placementRow("storage", "storage-view", StatusEnabled),
				placementRow("compute-1", "egress-proxy", StatusEnabled),
			},
			want: map[string]bool{"placement:control:monitor": true, "placement:storage:storage-view": true, "placement:compute-1:egress-proxy": true},
		},
		{
			name:  "部署停用时停用检查",
			hosts: productionHosts(),
			placements: []*adminpb.DeployPlacement{
				placementRow("control", "monitor", StatusEnabled),
				placementRow("storage", "storage-view", "disabled"),
				placementRow("compute-1", "egress-proxy", StatusEnabled),
			},
			want: map[string]bool{"placement:control:monitor": true, "placement:storage:storage-view": false, "placement:compute-1:egress-proxy": true},
		},
		{
			name: "主机停用时停用它上面的检查",
			hosts: []*adminpb.DeployHost{
				{HostId: "control", Address: "106.53.107.122", Status: StatusEnabled},
				{HostId: "storage", Address: "146.56.196.204", Status: StatusEnabled},
				{HostId: "compute-1", Address: "43.132.204.177", Status: "disabled"},
			},
			placements: []*adminpb.DeployPlacement{
				placementRow("control", "monitor", StatusEnabled),
				placementRow("storage", "storage-view", "disabled"),
				placementRow("compute-1", "egress-proxy", StatusEnabled),
			},
			want: map[string]bool{"placement:control:monitor": true, "placement:storage:storage-view": false, "placement:compute-1:egress-proxy": false},
		},
		{
			name:  "部署删除时删除检查",
			hosts: productionHosts(),
			placements: []*adminpb.DeployPlacement{
				placementRow("control", "monitor", StatusEnabled),
			},
			want: map[string]bool{"placement:control:monitor": true},
		},
	}
	for _, step := range steps {
		_, err := syncer.SyncPlacements(ctx, step.hosts, step.placements)
		require.NoError(t, err, step.name)
		checks, err := repos.Checks.ListBySource(ctx, domain.CheckSourcePlacement)
		require.NoError(t, err, step.name)
		got := map[string]bool{}
		for _, check := range checks {
			got[check.CheckID] = check.Enabled
		}
		require.Equal(t, step.want, got, step.name)
	}
}

func TestSyncerDeletesRulesAndResultsOfRemovedPlacement(t *testing.T) {
	repos := testRepositories(t)
	ctx := context.Background()
	syncer := NewSyncer(repos.Checks, nil, nil)
	_, err := syncer.SyncPlacements(ctx, productionHosts(), []*adminpb.DeployPlacement{placementRow("storage", "storage-view", StatusEnabled)})
	require.NoError(t, err)
	checkID := CheckID("storage", "storage-view")
	require.NoError(t, repos.Alerts.CreateRule(ctx, &domain.AlertRule{RuleID: "default:" + checkID, CheckID: checkID, FailureThreshold: 3, SuccessThreshold: 2, Enabled: true}))
	require.NoError(t, repos.Alerts.UpsertState(ctx, &domain.AlertState{RuleID: "default:" + checkID, CheckID: checkID, Status: domain.AlertStatusFiring, DedupeKey: "default:" + checkID}))
	require.NoError(t, repos.Results.Insert(ctx, &domain.CheckResult{ResultID: "r1", CheckID: checkID, Status: domain.CheckStatusDown}))

	_, err = syncer.SyncPlacements(ctx, productionHosts(), nil)
	require.NoError(t, err)
	_, exists := checkState(t, repos, "storage", "storage-view")
	require.False(t, exists)
	rules, err := repos.Alerts.ListRulesForCheck(ctx, "", checkID)
	require.NoError(t, err)
	require.Empty(t, rules, "删除检查时连同告警规则一起删除，否则告警会一直挂着")
	state, err := repos.Alerts.GetState(ctx, "", "default:"+checkID, checkID)
	require.Error(t, err, "告警状态也应删除: %+v", state)
	results, err := repos.Results.Recent(ctx, "", checkID, 10)
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestSyncerBuildsProbeFromCatalogHealth(t *testing.T) {
	repos := testRepositories(t)
	syncer := NewSyncer(repos.Checks, nil, nil)
	_, err := syncer.SyncPlacements(context.Background(), productionHosts(), []*adminpb.DeployPlacement{
		placementRow("control", "monitor", StatusEnabled),
		placementRow("control", "console-proxy", StatusEnabled),
		placementRow("storage", "storage-primary", StatusEnabled),
		placementRow("storage", "host-gateway", StatusEnabled),
	})
	require.NoError(t, err)

	monitor, ok := checkState(t, repos, "control", "monitor")
	require.True(t, ok)
	require.Equal(t, "http://127.0.0.1:11409/readyz", monitor.URL, "control 上的组件走回环地址")
	require.Equal(t, `"ready":true`, monitor.BodyContains)
	require.Equal(t, domain.CheckSourcePlacement, monitor.Source)
	require.Equal(t, `{"host_id":"control","component_id":"monitor"}`, monitor.Labels)
	require.Contains(t, monitor.Name, "（control）")

	proxy, ok := checkState(t, repos, "control", "console-proxy")
	require.True(t, ok)
	require.Equal(t, "https://106.53.107.122:9527/", proxy.URL, "https 方式按公网地址访问，证书签给公网地址")
	require.Equal(t, "200-399", proxy.ExpectedStatus)
	require.Empty(t, proxy.BodyContains)

	primary, ok := checkState(t, repos, "storage", "storage-primary")
	require.True(t, ok)
	require.Equal(t, "http://146.56.196.204:20210/readyz", primary.URL, "其他主机按公网地址探测")

	gateway, ok := checkState(t, repos, "storage", "host-gateway")
	require.True(t, ok)
	require.Equal(t, "http://146.56.196.204:11012/readyz", gateway.URL, "主机网关本身也是一条部署，一并探测")
}

func TestSyncerKeepsCheckWhenDefinitionIsTemporarilyInvalid(t *testing.T) {
	repos := testRepositories(t)
	ctx := context.Background()
	syncer := NewSyncer(repos.Checks, nil, nil)
	_, err := syncer.SyncPlacements(ctx, productionHosts(), []*adminpb.DeployPlacement{placementRow("storage", "storage-view", StatusEnabled)})
	require.NoError(t, err)
	// 主机记录暂时缺失：报告错误，但不能把检查当作已删除。
	_, err = syncer.SyncPlacements(ctx, nil, []*adminpb.DeployPlacement{placementRow("storage", "storage-view", StatusEnabled)})
	require.ErrorContains(t, err, "主机不存在")
	_, exists := checkState(t, repos, "storage", "storage-view")
	require.True(t, exists)

	_, err = syncer.SyncPlacements(ctx, productionHosts(), []*adminpb.DeployPlacement{placementRow("storage", "unknown-component", StatusEnabled)})
	require.ErrorContains(t, err, "不在组件目录中")
}

func TestCheckIDRoundTrip(t *testing.T) {
	host, component, ok := ParseCheckID(CheckID("compute-1", "egress-proxy"))
	require.True(t, ok)
	require.Equal(t, "compute-1", host)
	require.Equal(t, "egress-proxy", component)
	for _, id := range []string{"sysdeploy:control:monitor", "placement:control", "placement::monitor", "kline_freshness:x"} {
		_, _, ok := ParseCheckID(id)
		require.False(t, ok, id)
	}
}

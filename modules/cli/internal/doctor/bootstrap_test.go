package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
)

type deploymentClientStub struct{ rows []Deployment }

func (s deploymentClientStub) ListDeployments(context.Context, string) ([]Deployment, error) {
	return s.rows, nil
}

type overviewClientStub struct {
	overview    *monitorpb.HealthOverview
	err         error
	lastSpaceID *string
}

func (s overviewClientStub) GetHealthOverview(_ context.Context, spaceID string) (*monitorpb.HealthOverview, error) {
	if s.lastSpaceID != nil {
		*s.lastSpaceID = spaceID
	}
	return s.overview, s.err
}

func writeVersion(t *testing.T, root, checksum string) {
	t.Helper()
	raw := fmt.Sprintf(`{"version":"dev","commit":"dev","catalog_sha256":%q,"deployed_at":"2026-11-15T00:00:00Z","components":{}}`, checksum)
	require.NoError(t, os.WriteFile(filepath.Join(root, "version.json"), []byte(raw), 0o600))
}

func TestRunBootstrapInventory(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	rows := make([]Deployment, 0, len(manifest.Components))
	for _, component := range manifest.Components {
		rows = append(rows, Deployment{ComponentID: component.ComponentID, HostID: "node-a", Status: "enabled"})
	}
	writeVersion(t, root, manifest.Checksum)
	report, err := RunBootstrap(context.Background(), BootstrapOptions{NodeID: "node-a", LocalNodeID: "node-a", DeployRoot: root, CheckIDs: []string{"bootstrap.inventory"}, Client: deploymentClientStub{rows: rows}})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Len(t, report.Checks, 2)
}

// 部署记录的服务目录摘要与 CLI 内嵌的不一致、没有部署记录时，发布契约检查不通过；不再依赖数据集健康策略文件。
func TestRunBootstrapChecksDeployedCatalog(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	run := func(options BootstrapOptions) core.CheckStatus {
		options.NodeID, options.LocalNodeID = "node-a", "node-a"
		options.CheckIDs = []string{"bootstrap.release_contract"}
		report, err := RunBootstrap(context.Background(), options)
		require.NoError(t, err)
		return report.CheckByID("bootstrap.release_contract").Status
	}
	require.Equal(t, core.StatusFail, run(BootstrapOptions{DeployRoot: root}), "没有 version.json")
	writeVersion(t, root, "sha256:"+strings.Repeat("0", 64))
	require.Equal(t, core.StatusFail, run(BootstrapOptions{DeployRoot: root}), "摘要不一致")
	writeVersion(t, root, manifest.Checksum)
	require.Equal(t, core.StatusPass, run(BootstrapOptions{DeployRoot: root}))
	require.Equal(t, core.StatusFail, run(BootstrapOptions{}), "没有部署根")
	// 远端节点经注入的读取函数取 version.json。
	var asked string
	read := func(_ context.Context, node, path string) ([]byte, error) {
		asked = node + ":" + path
		return os.ReadFile(path)
	}
	require.Equal(t, core.StatusPass, run(BootstrapOptions{DeployRoot: root, ReadFile: read}))
	require.Equal(t, "node-a:"+filepath.Join(root, "version.json"), asked)
}

func TestRunBootstrapRemoteNodeRequiresRemoteCapabilities(t *testing.T) {
	_, err := RunBootstrap(context.Background(), BootstrapOptions{NodeID: "remote", LocalNodeID: "local"})
	require.ErrorContains(t, err, "远端 SSH 能力")

	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	writeVersion(t, root, manifest.Checksum)
	var readNode, readPath, probedNode, probedPath string
	versionRaw, err := os.ReadFile(filepath.Join(root, "version.json"))
	require.NoError(t, err)
	report, err := RunBootstrap(context.Background(), BootstrapOptions{
		NodeID:      "remote",
		LocalNodeID: "local",
		DeployRoot:  "/data/moox",
		CheckIDs:    []string{"bootstrap.path_permissions:host-gateway@remote", "bootstrap.network:host-gateway@remote", "service.health:host-gateway@remote"},
		Client: deploymentClientStub{rows: []Deployment{
			{ComponentID: "host-gateway", HostID: "remote", Status: "enabled"},
			{ComponentID: "host-agent", HostID: "remote", Status: "enabled"},
		}},
		ReadFile: func(_ context.Context, node, filePath string) ([]byte, error) {
			readNode, readPath = node, filePath
			return versionRaw, nil
		},
		Probe: func(_ context.Context, node string, _ int, _ string) (ProbeResult, error) {
			probedNode = node
			return ProbeResult{StatusCode: 200}, nil
		},
		ProbeWritable: func(_ context.Context, _, rel string) error {
			probedPath = rel
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Equal(t, "remote", readNode)
	require.Equal(t, "/data/moox/version.json", readPath)
	require.Equal(t, "remote", probedNode)
	require.Equal(t, "data/host-gateway", probedPath)
	require.Equal(t, "/data/moox/version.json", joinNodePath(true, "/data/moox/", "version.json"))
	require.Equal(t, "data/archive-worker", joinNodePath(true, "data", "archive-worker"))
}

// 自启动与 systemd 状态检查对每个组件都读 systemd 用户服务 moox-<组件>.service；数据目录检查写的是 <部署根>/data/<组件>。
func TestBootstrapRunnerUsesInjectedHostCapabilities(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	deployRoot := t.TempDir()
	var probed []string
	var units []string
	runner := &bootstrapRunner{
		manifest:    manifest,
		deployments: map[string]Deployment{"factor-mgr": {ComponentID: "factor-mgr", Status: "enabled"}, "factor-engine": {ComponentID: "factor-engine", Status: "enabled"}},
		options: BootstrapOptions{
			NodeID:        "node-a",
			DeployRoot:    deployRoot,
			ProbeWritable: func(_ context.Context, root, path string) error { probed = append(probed, root+"|"+path); return nil },
			Systemd: func(_ context.Context, node, unit string) (SystemdState, error) {
				units = append(units, node+"/"+unit)
				return SystemdState{Enabled: true, Active: true, Linger: true}, nil
			},
		},
	}
	for _, id := range []string{"factor-mgr", "factor-engine"} {
		require.Equal(t, core.StatusPass, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.path_permissions:" + id + "@node-a"}, nil).Status)
		require.Equal(t, core.StatusPass, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_autostart:" + id + "@node-a"}, nil).Status)
	}
	require.Equal(t, []string{deployRoot + "|data/factor-mgr", deployRoot + "|data/factor-engine"}, probed)
	require.Equal(t, []string{"node-a/moox-factor-mgr.service", "node-a/moox-factor-engine.service"}, units)

	// systemd 状态不全（active 但没开机自启）不通过；读不到时记为无法判断。
	runner.options.Systemd = func(context.Context, string, string) (SystemdState, error) {
		return SystemdState{Enabled: false, Active: true, Linger: true}, nil
	}
	require.Equal(t, core.StatusFail, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_autostart:factor-mgr@node-a"}, nil).Status)
	runner.options.Systemd = nil
	require.Equal(t, core.StatusUnknown, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_autostart:factor-mgr@node-a"}, nil).Status)
	runner.options.DeployRoot = ""
	require.Equal(t, core.StatusWarn, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.path_permissions:factor-mgr@node-a"}, nil).Status)
}

// 服务身份检查核对 service/instance_id/node_id/boot_id，不再比对 dataset_health_policy_hash。
func TestBootstrapServiceIdentityIgnoresDatasetHealthPolicyHash(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	runner := &bootstrapRunner{
		manifest:    manifest,
		deployments: map[string]Deployment{"monitor": {ComponentID: "monitor", Status: "enabled"}},
		options: BootstrapOptions{
			NodeID: "control",
			Probe: func(_ context.Context, _ string, _ int, path string) (ProbeResult, error) {
				require.Equal(t, "/readyz", path)
				return ProbeResult{
					StatusCode: 200,
					Body:       []byte(`{"service":"monitor","instance_id":"monitor@control","node_id":"control","boot_id":"boot-1","dataset_health_policy_hash":"sha256:stale"}`),
				}, nil
			},
		},
	}
	res := runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_identity:monitor@control"}, nil)
	require.Equal(t, core.StatusPass, res.Status)

	// 节点 SSH 不可达时记为无法判断（UNKNOWN），不是误判服务故障。
	runner.options.Probe = func(context.Context, string, int, string) (ProbeResult, error) {
		return ProbeResult{}, fmt.Errorf("%w: ssh refused", ErrNodeUnreachable)
	}
	require.Equal(t, core.StatusUnknown, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.service_identity:monitor@control"}, nil).Status)
	require.Equal(t, core.StatusUnknown, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.network:monitor@control"}, nil).Status)
	require.Equal(t, core.StatusUnknown, runner.run(context.Background(), core.CheckSpec{ID: "service.health:monitor@control"}, nil).Status)
}

// 本机健康端口探测：bootstrap.network 查 /healthz，service.health 查 /readyz。
func TestBootstrapProbesLocalHealthAndReadyPorts(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	var asked []string
	runner := &bootstrapRunner{
		manifest:    manifest,
		deployments: map[string]Deployment{"factor-mgr": {ComponentID: "factor-mgr", Status: "enabled"}},
		options: BootstrapOptions{NodeID: "node-a", Probe: func(_ context.Context, node string, port int, path string) (ProbeResult, error) {
			asked = append(asked, fmt.Sprintf("%s:%d%s", node, port, path))
			return ProbeResult{StatusCode: 200}, nil
		}},
	}
	require.Equal(t, core.StatusPass, runner.run(context.Background(), core.CheckSpec{ID: "bootstrap.network:factor-mgr@node-a"}, nil).Status)
	require.Equal(t, core.StatusPass, runner.run(context.Background(), core.CheckSpec{ID: "service.health:factor-mgr@node-a"}, nil).Status)
	require.Equal(t, []string{"node-a:11414/healthz", "node-a:11414/readyz"}, asked)
	runner.options.Probe = nil
	require.Equal(t, core.StatusFail, runner.run(context.Background(), core.CheckSpec{ID: "service.health:factor-mgr@node-a"}, nil).Status)
}

// 自检第二部分直接读取 Monitor 的 GetHealthOverview：检查组件上报、就绪与严重告警，不在 CLI 侧重算。
func TestRunBootstrapReadsMonitorHealthOverview(t *testing.T) {
	manifest, err := core.LoadEmbeddedManifest()
	require.NoError(t, err)
	root := t.TempDir()
	writeVersion(t, root, manifest.Checksum)
	deployments := []Deployment{
		{ComponentID: "admin", HostID: "control", Status: "enabled"},
		{ComponentID: "host-gateway", HostID: "control", Status: "enabled"},
		{ComponentID: "host-agent", HostID: "control", Status: "enabled"},
		{ComponentID: "monitor", HostID: "control", Status: "enabled"},
		{ComponentID: "collector", HostID: "control", Status: "enabled"},
	}
	baseOptions := func(overview *monitorpb.HealthOverview, overviewErr error) BootstrapOptions {
		return BootstrapOptions{
			NodeID:        "control",
			LocalNodeID:   "control",
			DeployRoot:    root,
			Client:        deploymentClientStub{rows: deployments},
			MonitorClient: overviewClientStub{overview: overview, err: overviewErr},
			Probe: func(context.Context, string, int, string) (ProbeResult, error) {
				return ProbeResult{StatusCode: 200}, nil
			},
			CheckIDs: []string{
				"monitor.component:collector@control",
				"monitor.component:monitor@control",
				"monitor.alerts:control",
			},
		}
	}

	// 1. 全部已上报且就绪，仅有一条其他主机的严重告警和本节点 warning：本节点通过。
	healthyOverview := &monitorpb.HealthOverview{
		TopologyKnown: true,
		Components: []*monitorpb.HealthComponent{
			{HostId: "control", ComponentId: "monitor", Status: "healthy", Readiness: "ready", Reporter: &monitorpb.HealthSignal{Status: "healthy"}},
			{HostId: "control", ComponentId: "collector", Status: "healthy", Readiness: "ready", Reporter: &monitorpb.HealthSignal{Status: "healthy"}},
		},
		Alerts: []*monitorpb.HealthAlert{
			{Id: "other", Severity: "critical", Title: "海外主机异常", Object: &monitorpb.HealthObject{Type: "host", HostId: "overseas"}},
			{Id: "warn", Severity: "warning", Title: "资源偏高", Object: &monitorpb.HealthObject{Type: "host", HostId: "control"}},
		},
	}
	report, err := RunBootstrap(context.Background(), baseOptions(healthyOverview, nil))
	require.NoError(t, err)
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Equal(t, core.StatusPass, report.CheckByID("monitor.component:collector@control").Status)
	require.Equal(t, core.StatusPass, report.CheckByID("monitor.alerts:control").Status)

	// 2. 组件未就绪或停止上报、以及本节点严重告警：直接反映为 FAIL。
	unhealthyOverview := &monitorpb.HealthOverview{
		TopologyKnown: true,
		Components: []*monitorpb.HealthComponent{
			{HostId: "control", ComponentId: "monitor", Status: "healthy", Readiness: "ready", Reporter: &monitorpb.HealthSignal{Status: "healthy"}},
			{HostId: "control", ComponentId: "collector", Status: "down", Readiness: "not_ready", NotReadyReason: "存储连接未就绪", Reporter: &monitorpb.HealthSignal{Status: "healthy"}},
		},
		Alerts: []*monitorpb.HealthAlert{
			{Id: "crit", Severity: "critical", Title: "采集服务未就绪", Reason: "存储连接未就绪", Object: &monitorpb.HealthObject{Type: "component", HostId: "control", ComponentId: "collector"}},
		},
	}
	report, err = RunBootstrap(context.Background(), baseOptions(unhealthyOverview, nil))
	require.NoError(t, err)
	require.Equal(t, core.ConclusionUnhealthy, report.Conclusion)
	require.Equal(t, core.StatusFail, report.CheckByID("monitor.component:collector@control").Status)
	require.Contains(t, report.CheckByID("monitor.component:collector@control").Summary, "存储连接未就绪")
	require.Equal(t, core.StatusFail, report.CheckByID("monitor.alerts:control").Status)
	require.Contains(t, report.CheckByID("monitor.alerts:control").Summary, "采集服务未就绪")

	// 3. Monitor 概览不可读时，monitor.overview 记 UNKNOWN，下游 monitor.* 自动 BLOCKED。
	report, err = RunBootstrap(context.Background(), baseOptions(nil, errors.New("monitor unreachable")))
	require.NoError(t, err)
	require.Equal(t, core.ConclusionInconclusive, report.Conclusion)
	require.Equal(t, core.StatusUnknown, report.CheckByID("monitor.overview").Status)
	require.Equal(t, core.StatusBlocked, report.CheckByID("monitor.component:collector@control").Status)
	require.Equal(t, core.StatusBlocked, report.CheckByID("monitor.alerts:control").Status)

	// 4. 新装后暂停态组件通过自检；请求只查 mooxsys 空间，业务空间的严重告警（未启动空间标的或首根 K 线未闭合的行情探针）不阻塞安装自检。
	var requestedSpace string
	freshOpts := BootstrapOptions{
		NodeID:        "control",
		LocalNodeID:   "control",
		DeployRoot:    root,
		Client:        deploymentClientStub{rows: append(deployments, Deployment{ComponentID: "strategy", HostID: "control", Status: "enabled"})},
		MonitorClient: overviewClientStub{
			lastSpaceID: &requestedSpace,
			overview: &monitorpb.HealthOverview{
				TopologyKnown: true,
				Components: []*monitorpb.HealthComponent{
					{HostId: "control", ComponentId: "monitor", Status: "healthy", Readiness: "ready", Reporter: &monitorpb.HealthSignal{Status: "healthy"}},
					{HostId: "control", ComponentId: "collector", Status: "healthy", Readiness: "ready", Reporter: &monitorpb.HealthSignal{Status: "healthy"}},
					{
						HostId: "control", ComponentId: "strategy", Status: "healthy", Readiness: "ready",
						Reporter:  &monitorpb.HealthSignal{Status: "healthy"},
						Lifecycle: &commonpb.LifecycleSnapshot{Stage: "paused"},
					},
				},
				Alerts: []*monitorpb.HealthAlert{
					{Id: "stockcn-tag", Severity: "critical", Title: "A 股标的标签为空", Object: &monitorpb.HealthObject{Type: "subject_tag", SpaceId: "stockcn"}},
					{Id: "crypto-canary", Severity: "critical", Title: "行情探针无 K 线", Object: &monitorpb.HealthObject{Type: "market_canary", SpaceId: "crypto"}},
				},
			},
		},
		Probe: func(context.Context, string, int, string) (ProbeResult, error) {
			return ProbeResult{StatusCode: 200}, nil
		},
		CheckIDs: []string{
			"monitor.component:strategy@control",
			"monitor.alerts:control",
		},
	}
	report, err = RunBootstrap(context.Background(), freshOpts)
	require.NoError(t, err)
	require.Equal(t, events.SystemSpaceID, requestedSpace, "自检应只请求 mooxsys 系统空间概览")
	require.Equal(t, core.ConclusionHealthy, report.Conclusion)
	require.Equal(t, core.StatusPass, report.CheckByID("monitor.component:strategy@control").Status)
	require.Equal(t, core.StatusPass, report.CheckByID("monitor.alerts:control").Status)
}

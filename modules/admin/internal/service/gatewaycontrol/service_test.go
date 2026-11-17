package gatewaycontrol

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type controlFixture struct {
	service  *Service
	db       *gorm.DB
	path     string
	keys     *keys.Store
	topology *sysdeploy.TopologyDAO
	clock    atomic.Int64
}

func openControlDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	return db
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	dir := t.TempDir()
	f := &controlFixture{path: filepath.Join(dir, "admin.db")}
	f.db = openControlDB(t, f.path)
	require.NoError(t, f.db.Exec(schema.AdminSQL()).Error)
	var err error
	f.topology, err = sysdeploy.NewTopologyDAO(f.db, "control")
	require.NoError(t, err)
	require.NoError(t, f.topology.SyncHosts(context.Background(), []sysdeploy.HostSpec{
		{HostID: "control", Address: "control.example.test", Components: []string{"admin", "collector"}},
		{HostID: "storage", Address: "storage.example.test", Components: []string{"storage-primary", "storage-view"}},
		{HostID: "overseas", Address: "compute.example.test", Components: []string{"egress-proxy", "trade"}},
	}))
	f.keys, err = keys.NewStore(f.db)
	require.NoError(t, err)
	// 三台主机都签发过身份包，操作电脑登记了 moox-cli。
	for _, host := range []string{"control", "storage", "overseas"} {
		require.NoError(t, f.keys.SetHostPublicKey(context.Background(), host, publicKey("host/"+host)))
	}
	require.NoError(t, f.keys.SetIdentity(context.Background(), "moox-cli", publicKey("moox-cli")))
	f.service = f.newService(t, f.db)
	f.clock.Store(time.Now().UnixNano())
	f.service.now = func() time.Time { return time.Unix(0, f.clock.Load()) }
	return f
}

func (f *controlFixture) newService(t *testing.T, db *gorm.DB) *Service {
	t.Helper()
	service, err := NewService(db, "control")
	require.NoError(t, err)
	return service
}

func publicKey(label string) string {
	return gatewayauth.EncodePublicKey(authtest.Key(label).Public().(ed25519.PublicKey))
}

// hostContext 模拟网关验过签名之后的上下文：签名主机是 host。
func hostContext(host string) context.Context {
	return context.WithValue(context.Background(), verifiedHost{}, host)
}

func snapshotHost(snapshot *pb.HostGatewaySnapshot, id string) *pb.SnapshotHost {
	for _, host := range snapshot.Hosts {
		if host.HostId == id {
			return host
		}
	}
	return nil
}

func snapshotIdentity(snapshot *pb.HostGatewaySnapshot, identity string) *pb.SnapshotIdentity {
	for _, entry := range snapshot.Identities {
		if entry.Identity == identity {
			return entry
		}
	}
	return nil
}

func snapshotDeployment(snapshot *pb.HostGatewaySnapshot, host, component string) *pb.SnapshotDeployment {
	for _, deployment := range snapshot.Deployments {
		if deployment.HostId == host && deployment.ComponentId == component {
			return deployment
		}
	}
	return nil
}

// snapshotTopology 照网关的做法把快照里的主机与部署还原成拓扑。
func snapshotTopology(t *testing.T, snapshot *pb.HostGatewaySnapshot) servicecatalog.Topology {
	t.Helper()
	status := map[bool]string{true: servicecatalog.Enabled, false: servicecatalog.Disabled}
	var hosts []servicecatalog.Host
	for _, host := range snapshot.Hosts {
		reachability, ok := servicecatalog.ParseReachability(host.Reachability)
		require.True(t, ok, host.Reachability)
		hosts = append(hosts, servicecatalog.Host{ID: host.HostId, Address: host.Address, PrivateAddress: host.PrivateAddress, Status: status[host.Enabled], Reachability: reachability})
	}
	var deployments []servicecatalog.Deployment
	for _, deployment := range snapshot.Deployments {
		deployments = append(deployments, servicecatalog.Deployment{HostID: deployment.HostId, ComponentID: deployment.ComponentId, Status: status[deployment.Enabled]})
	}
	return servicecatalog.NewTopology(hosts, deployments)
}

// signingCallers 是各网关拿到快照后按内嵌目录算出的、每台主机上可以用主机密钥签名的调用方。
func signingCallers(t *testing.T, snapshot *pb.HostGatewaySnapshot) map[string][]string {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	return catalog.SigningCallers(snapshotTopology(t, snapshot))
}

func (f *controlFixture) snapshot(t *testing.T, host string) *pb.HostGatewaySnapshot {
	t.Helper()
	rsp, err := f.service.PullSnapshot(hostContext(host), &pb.PullSnapshotReq{HostId: host})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.True(t, rsp.Changed)
	require.NotNil(t, rsp.Snapshot)
	return rsp.Snapshot
}

// 快照原样带出登记的主机、部署与公钥，全部主机拿到同一份，不含路由；网关按内嵌目录从中算出每台主机可签名的调用方。
// 换公钥就换快照，没有新旧并存。
func TestSnapshotsCarryHostsDeploymentsAndPublicKeys(t *testing.T) {
	f := newControlFixture(t)
	ctx := context.Background()
	storage := f.snapshot(t, "storage")
	require.NotEmpty(t, storage.Hash)
	hash, err := pb.SnapshotHash(storage)
	require.NoError(t, err)
	require.Equal(t, hash, storage.Hash)
	require.EqualValues(t, sysdeploy.SnapshotSchemaVersion, storage.SchemaVersion)
	require.EqualValues(t, 2, storage.SchemaVersion)
	require.True(t, proto.Equal(storage, f.snapshot(t, "control")), "全部主机拿到同一份快照")
	require.True(t, proto.Equal(storage, f.snapshot(t, "overseas")), "全部主机拿到同一份快照")

	require.Len(t, storage.Hosts, 3)
	control := snapshotHost(storage, "control")
	require.Equal(t, "control.example.test", control.Address)
	require.Equal(t, servicecatalog.ReachabilityDirect, control.Reachability)
	require.True(t, control.Enabled)
	require.Equal(t, publicKey("host/control"), gatewayauth.EncodePublicKey(control.SignPublicKey))
	require.Equal(t, publicKey("host/overseas"), gatewayauth.EncodePublicKey(snapshotHost(storage, "overseas").SignPublicKey))
	// 部署原样列出：登记的组件加每台主机自动带的网关与 host-agent。
	require.Len(t, storage.Deployments, 2+2+2+3*2)
	for host, components := range map[string][]string{
		"control": {"admin", "collector", "host-agent", "host-gateway"}, "storage": {"storage-primary", "storage-view", "host-agent", "host-gateway"},
		"overseas": {"egress-proxy", "trade", "host-agent", "host-gateway"},
	} {
		for _, component := range components {
			deployment := snapshotDeployment(storage, host, component)
			require.NotNil(t, deployment, "%s/%s", host, component)
			require.True(t, deployment.Enabled)
		}
	}
	require.Len(t, storage.Identities, 1)
	require.Equal(t, publicKey("moox-cli"), gatewayauth.EncodePublicKey(snapshotIdentity(storage, "moox-cli").SignPublicKey))
	// 网关据此算出的调用名单：主机密钥只能替部署在它上面的调用方签名，console 只在部署了 admin 的主机上。
	callers := signingCallers(t, storage)
	require.Equal(t, []string{"admin", "collector", "console", "host-agent", "host-gateway"}, callers["control"])
	require.Equal(t, []string{"egress-proxy", "host-agent", "host-gateway", "trade"}, callers["overseas"])
	for host, list := range callers {
		require.NotContains(t, list, "moox-cli", "非主机身份不能出现在主机 %s 的调用方里", host)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	topology := snapshotTopology(t, storage)
	require.Equal(t, "control", topology.ControlHostID)
	for _, host := range storage.Hosts {
		compiled, err := catalog.Compile(topology, host.HostId)
		require.NoError(t, err)
		require.Empty(t, compiled.Unknown)
		require.NotEmpty(t, compiled.Routes, "网关要能从快照算出 %s 的路由", host.HostId)
	}
	unchanged, err := f.service.PullSnapshot(hostContext("storage"), &pb.PullSnapshotReq{HostId: "storage", CurrentHash: storage.Hash})
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Nil(t, unchanged.Snapshot)

	// 换操作电脑：moox-cli 的公钥被替换，快照里只剩新的那把。
	require.NoError(t, f.keys.SetIdentity(ctx, "moox-cli", publicKey("moox-cli-new")))
	replaced := f.snapshot(t, "storage")
	require.NotEqual(t, storage.Hash, replaced.Hash)
	require.Len(t, replaced.Identities, 1)
	require.Equal(t, publicKey("moox-cli-new"), gatewayauth.EncodePublicKey(snapshotIdentity(replaced, "moox-cli").SignPublicKey))
	// 换主机密钥同理。
	require.NoError(t, f.keys.SetHostPublicKey(ctx, "overseas", publicKey("host/overseas-new")))
	rotated := f.snapshot(t, "storage")
	require.NotEqual(t, replaced.Hash, rotated.Hash)
	require.Equal(t, publicKey("host/overseas-new"), gatewayauth.EncodePublicKey(snapshotHost(rotated, "overseas").SignPublicKey))
	// 外部身份的公钥也在快照里：外部入口（11004）用它验签；11002、11003 上它们调不了任何方法。
	require.NoError(t, f.keys.SetIdentity(ctx, "moox-skill", publicKey("moox-skill")))
	require.NotNil(t, snapshotIdentity(f.snapshot(t, "storage"), "moox-skill"))
	// 外部入口的登记随快照下发；只能出站的主机不能登记成外部入口。
	require.False(t, snapshotHost(f.snapshot(t, "storage"), "storage").ExternalEntry)
	entry := sysdeploy.HostSpec{HostID: "storage", Address: "storage.example.test", ExternalEntry: true, Components: []string{"storage-primary", "storage-view"}}
	require.NoError(t, f.topology.SyncHostDeployments(ctx, entry))
	require.True(t, snapshotHost(f.snapshot(t, "storage"), "storage").ExternalEntry)
	require.False(t, snapshotHost(f.snapshot(t, "storage"), "overseas").ExternalEntry)
	entry.Reachability = servicecatalog.ReachabilityOutbound
	require.ErrorIs(t, f.topology.SyncHostDeployments(ctx, entry), sysdeploy.ErrInvalidTopology)
	// 还没签发过身份包的主机没有公钥，但主机本身仍在快照里。
	require.NoError(t, f.db.Exec("UPDATE t_hosts SET c_sign_public_key = '' WHERE c_host_id = 'overseas'").Error)
	require.Empty(t, snapshotHost(f.snapshot(t, "storage"), "overseas").SignPublicKey)
	require.NoError(t, f.keys.SetHostPublicKey(ctx, "overseas", publicKey("host/overseas")))

	// 停用的主机：它仍能取到快照（从中得知自己已停用），部署原样保留；网关算出它只剩 host-gateway 这一个调用方，它上面的组件不能再签名。
	before := f.snapshot(t, "storage")
	require.NoError(t, f.topology.SetHostStatus(ctx, "storage", servicecatalog.Disabled))
	disabled := f.snapshot(t, "storage")
	require.NotEqual(t, before.Hash, disabled.Hash)
	require.False(t, snapshotHost(disabled, "storage").Enabled)
	require.True(t, snapshotHost(disabled, "control").Enabled)
	require.NotNil(t, snapshotDeployment(disabled, "storage", "storage-primary"))
	require.NotEmpty(t, snapshotHost(disabled, "storage").SignPublicKey)
	require.Equal(t, []string{"host-gateway"}, signingCallers(t, disabled)["storage"])
	compiled, err := catalog.Compile(snapshotTopology(t, disabled), "storage")
	require.NoError(t, err)
	require.True(t, compiled.Disabled)
	require.Empty(t, compiled.Routes)
	require.NotContains(t, compiled.Directory.Hosts, "storage")

	// 一台主机只能以自己的名义取快照；没有验过的签名主机时拒绝；没登记的主机拿不到。
	_, err = f.service.PullSnapshot(hostContext("control"), &pb.PullSnapshotReq{HostId: "storage"})
	require.Error(t, err)
	_, err = f.service.PullSnapshot(ctx, &pb.PullSnapshotReq{HostId: "control"})
	require.Error(t, err)
	unknown, err := f.service.PullSnapshot(hostContext("ghost"), &pb.PullSnapshotReq{HostId: "ghost"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, unknown.GetRetInfo().GetCode())
	require.Nil(t, unknown.Snapshot)
	report, err := f.service.ReportStatus(hostContext("ghost"), &pb.ReportStatusReq{HostId: "ghost", InstanceId: "G", Version: "v1"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, report.GetRetInfo().GetCode())
}

func TestSnapshotReadsTopologyAndKeysFromOneDatabaseState(t *testing.T) {
	f := newControlFixture(t)
	before := f.snapshot(t, "storage")
	writerDB := openControlDB(t, f.path)
	var changed atomic.Bool
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register("fixture:rotate-after-topology", func(tx *gorm.DB) {
		if tx.Statement.Table != "t_deployments" || !changed.CompareAndSwap(false, true) {
			return
		}
		require.NoError(t, writerDB.Transaction(func(writer *gorm.DB) error {
			if err := writer.Exec("UPDATE t_hosts SET c_address='new.storage.example.test' WHERE c_host_id='storage'").Error; err != nil {
				return err
			}
			store, err := keys.NewStore(writer)
			if err != nil {
				return err
			}
			return store.SetIdentity(context.Background(), "moox-skill", publicKey("moox-skill"))
		}))
	}))
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove("fixture:rotate-after-topology") })
	during := f.snapshot(t, "storage")
	require.True(t, changed.Load())
	require.Equal(t, before.Hash, during.Hash, "mid-read commits must not mix old hosts with new keys")
	after := f.snapshot(t, "storage")
	require.NotEqual(t, before.Hash, after.Hash)
	require.Equal(t, "storage.example.test", snapshotHost(during, "storage").Address)
	require.Nil(t, snapshotIdentity(during, "moox-skill"))
	require.Equal(t, "new.storage.example.test", snapshotHost(after, "storage").Address)
	require.NotNil(t, snapshotIdentity(after, "moox-skill"))
}

func (f *controlFixture) report(t *testing.T, instance string) {
	t.Helper()
	rsp, err := f.service.ReportStatus(hostContext("storage"), &pb.ReportStatusReq{HostId: "storage", InstanceId: instance, Version: "v-test-" + instance, RouteCount: 7, AppliedHash: strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func (f *controlFixture) status(t *testing.T) sysdeploy.HostGatewayStatus {
	t.Helper()
	var status sysdeploy.HostGatewayStatus
	require.NoError(t, f.db.Where("c_host_id='storage'").First(&status).Error)
	return status
}

func TestHeartbeatsDistinguishReplacementConflictAndExpiry(t *testing.T) {
	f := newControlFixture(t)
	f.report(t, "A")
	initial := f.status(t)
	require.Equal(t, f.snapshot(t, "storage").Hash, initial.ExpectedHash)
	require.Empty(t, initial.PreviousInstanceID)
	f.clock.Add(int64(time.Minute))
	f.report(t, "B")
	replaced := f.status(t)
	require.Equal(t, "A", replaced.PreviousInstanceID)
	require.NotNil(t, replaced.ReplacedAt)
	require.Empty(t, replaced.ConflictInstanceID)
	f.clock.Add(int64(time.Minute))
	f.report(t, "A")
	conflict := f.status(t)
	require.Equal(t, "B", conflict.ConflictInstanceID)
	require.NotNil(t, conflict.ConflictSeenAt)
	f.clock.Add(int64(time.Minute))
	f.report(t, "B")
	renewed := f.status(t)
	require.Equal(t, "A", renewed.ConflictInstanceID)
	require.True(t, renewed.ConflictSeenAt.After(*conflict.ConflictSeenAt))
	f.clock.Add(int64(5*time.Minute - time.Nanosecond))
	f.report(t, "B")
	require.NotEmpty(t, f.status(t).ConflictInstanceID)
	f.clock.Add(1)
	f.report(t, "B")
	require.Empty(t, f.status(t).ConflictInstanceID)
	require.Nil(t, f.status(t).ConflictSeenAt)
	f.report(t, "C")
	require.Empty(t, f.status(t).ConflictInstanceID)
	// Reads expire old conflicts even when the gateway stops heartbeating.
	past := time.Now().Add(-6 * time.Minute)
	require.NoError(t, f.db.Model(&sysdeploy.HostGatewayStatus{}).Where("c_host_id='storage'").Updates(map[string]any{"c_conflict_instance_id": "B", "c_conflict_seen_at": &past}).Error)
	read, err := f.topology.GetGatewayStatus(context.Background(), "storage")
	require.NoError(t, err)
	require.Empty(t, read.ConflictInstanceID)
	require.Nil(t, read.ConflictSeenAt)
}

func TestInvalidHeartbeatsDoNotOverwriteLastGoodStatus(t *testing.T) {
	f := newControlFixture(t)
	f.report(t, "A")
	before := f.status(t)
	for _, mutate := range []func(*pb.ReportStatusReq){
		func(r *pb.ReportStatusReq) { r.InstanceId = "" }, func(r *pb.ReportStatusReq) { r.InstanceId = "../bad" },
		func(r *pb.ReportStatusReq) { r.Version = "" }, func(r *pb.ReportStatusReq) { r.AppliedHash = "bad" },
		func(r *pb.ReportStatusReq) { r.RouteCount = -1 }, func(r *pb.ReportStatusReq) { r.RouteCount = 10001 },
		func(r *pb.ReportStatusReq) { r.Error = strings.Repeat("x", 4097) },
		// 只有 control 网关能上报反向连接会话。
		func(r *pb.ReportStatusReq) {
			r.RelaySessions = []*pb.RelaySessionStatus{{HostId: "home", Online: true}}
		},
	} {
		req := &pb.ReportStatusReq{HostId: "storage", InstanceId: "B", Version: "v2"}
		mutate(req)
		rsp, err := f.service.ReportStatus(hostContext("storage"), req)
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
		require.Equal(t, before, f.status(t))
	}
	_, err := f.service.ReportStatus(hostContext("control"), &pb.ReportStatusReq{HostId: "storage", InstanceId: "B", Version: "v2"})
	require.Error(t, err)
	require.Equal(t, before, f.status(t))
}

func TestIndependentStatusWritersSerializeInstanceHistory(t *testing.T) {
	f := newControlFixture(t)
	f.report(t, "A")
	other := f.newService(t, openControlDB(t, f.path))
	var wg sync.WaitGroup
	responses := make([]*pb.ReportStatusRsp, 2)
	errorsSeen := make([]error, 2)
	start := make(chan struct{})
	for i, service := range []*Service{f.service, other} {
		wg.Add(1)
		go func(i int, service *Service) {
			defer wg.Done()
			<-start
			instance := []string{"B", "C"}[i]
			responses[i], errorsSeen[i] = service.ReportStatus(hostContext("storage"), &pb.ReportStatusReq{HostId: "storage", InstanceId: instance, Version: "v-" + instance})
		}(i, service)
	}
	close(start)
	wg.Wait()
	for i, response := range responses {
		require.NoError(t, errorsSeen[i])
		require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	}
	status := f.status(t)
	require.Contains(t, []string{"B", "C"}, status.InstanceID)
	require.Contains(t, []string{"B", "C"}, status.PreviousInstanceID)
	require.NotEqual(t, status.InstanceID, status.PreviousInstanceID)
	require.Equal(t, "v-"+status.InstanceID, status.Version)
	require.Empty(t, status.ConflictInstanceID)
}

// 新登记、还没签发身份包的主机不影响别的主机取快照，读快照也不会替它生成密钥；签发之后它的公钥才进快照。
func TestNewHostWithoutKeyDoesNotBlockSnapshots(t *testing.T) {
	f := newControlFixture(t)
	ctx := context.Background()
	before := f.snapshot(t, "control")
	require.NoError(t, f.topology.SyncHostDeployments(ctx, sysdeploy.HostSpec{HostID: "new-host", Address: "new.example.test"}))
	pending := f.snapshot(t, "control")
	require.NotEqual(t, before.Hash, pending.Hash)
	require.NotNil(t, snapshotHost(pending, "new-host"))
	require.Empty(t, snapshotHost(pending, "new-host").SignPublicKey)
	key, err := f.keys.HostPublicKey(ctx, "new-host")
	require.NoError(t, err)
	require.Empty(t, key, "读快照不生成密钥")
	require.NoError(t, f.keys.SetHostPublicKey(ctx, "new-host", publicKey("host/new-host")))
	issued := f.snapshot(t, "control")
	require.Equal(t, publicKey("host/new-host"), gatewayauth.EncodePublicKey(snapshotHost(issued, "new-host").SignPublicKey))
	require.Equal(t, []string{"host-agent", "host-gateway"}, signingCallers(t, issued)["new-host"])
	_, err = NewService(f.db, "old_node")
	require.Error(t, err)
}

func TestFailedStatusWritePreservesInstanceHistory(t *testing.T) {
	f := newControlFixture(t)
	f.report(t, "A")
	before := f.status(t)
	require.NoError(t, f.db.Exec("CREATE TRIGGER reject_status BEFORE INSERT ON t_host_gateway_status BEGIN SELECT RAISE(ABORT, 'fixture rejection'); END").Error)
	response, err := f.service.ReportStatus(hostContext("storage"), &pb.ReportStatusReq{HostId: "storage", InstanceId: "B", Version: "v-new"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, response.GetRetInfo().GetCode())
	require.Equal(t, before, f.status(t))
}

// control 网关的心跳携带反向连接会话，经主机状态查询原样返回；非法会话拒绝。
func TestControlHeartbeatCarriesRelaySessions(t *testing.T) {
	f := newControlFixture(t)
	report := func(sessions []*pb.RelaySessionStatus) *pb.ReportStatusRsp {
		rsp, err := f.service.ReportStatus(hostContext("control"), &pb.ReportStatusReq{HostId: "control", InstanceId: "C", Version: "v1", RelaySessions: sessions})
		require.NoError(t, err)
		return rsp
	}
	sessions := []*pb.RelaySessionStatus{{HostId: "home", Online: true, InstanceId: "abc", ConnectedAt: "2026-10-18T01:00:00Z", Reconnects: 2, Streams: 1}}
	require.Equal(t, pb.ErrorCode_SUCCESS, report(sessions).GetRetInfo().GetCode())
	var status sysdeploy.HostGatewayStatus
	require.NoError(t, f.db.Where("c_host_id = ?", "control").First(&status).Error)
	require.Contains(t, status.RelaySessions, `"host_id":"home"`)
	for _, bad := range [][]*pb.RelaySessionStatus{
		{{HostId: "home"}, {HostId: "home"}}, {{HostId: "bad/id"}}, {{HostId: "home", ConnectedAt: "yesterday"}}, {{HostId: "home", Reconnects: -1}},
	} {
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, report(bad).GetRetInfo().GetCode())
	}
}

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
		{HostID: "overseas", Address: "compute.example.test", Components: []string{"access", "trade"}},
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

func verificationKey(snapshot *pb.HostGatewaySnapshot, host, identity string) *pb.GatewayVerificationKey {
	for _, key := range snapshot.VerificationKeys {
		if key.HostId == host && key.Identity == identity {
			return key
		}
	}
	return nil
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

// 快照里只有公钥：每台主机一把，连同部署在它上面的调用方；外加不属于主机的身份。换公钥就换快照，没有新旧并存。
func TestSnapshotsCarryPublicKeysByHostAndIdentity(t *testing.T) {
	f := newControlFixture(t)
	ctx := context.Background()
	storage := f.snapshot(t, "storage")
	require.NotEmpty(t, storage.Hash)
	require.Len(t, storage.VerificationKeys, 4)
	control := verificationKey(storage, "control", "")
	require.Equal(t, publicKey("host/control"), gatewayauth.EncodePublicKey(control.PublicKey))
	require.Equal(t, []string{"admin", "collector", "console", "host-agent", "host-gateway"}, control.Callers)
	require.Equal(t, []string{"access", "host-agent", "host-gateway", "trade"}, verificationKey(storage, "overseas", "").Callers)
	require.Equal(t, publicKey("moox-cli"), gatewayauth.EncodePublicKey(verificationKey(storage, "", "moox-cli").PublicKey))
	for _, key := range storage.VerificationKeys {
		require.NotContains(t, key.Callers, "moox-cli", "非主机身份不能出现在主机的调用方里")
	}
	unchanged, err := f.service.PullSnapshot(hostContext("storage"), &pb.PullSnapshotReq{HostId: "storage", CurrentHash: storage.Hash})
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Nil(t, unchanged.Snapshot)

	// 换操作电脑：moox-cli 的公钥被替换，快照里只剩新的那把。
	require.NoError(t, f.keys.SetIdentity(ctx, "moox-cli", publicKey("moox-cli-new")))
	replaced := f.snapshot(t, "storage")
	require.NotEqual(t, storage.Hash, replaced.Hash)
	require.Len(t, replaced.VerificationKeys, 4)
	require.Equal(t, publicKey("moox-cli-new"), gatewayauth.EncodePublicKey(verificationKey(replaced, "", "moox-cli").PublicKey))
	// 换主机密钥同理。
	require.NoError(t, f.keys.SetHostPublicKey(ctx, "overseas", publicKey("host/overseas-new")))
	rotated := f.snapshot(t, "storage")
	require.NotEqual(t, replaced.Hash, rotated.Hash)
	require.Equal(t, publicKey("host/overseas-new"), gatewayauth.EncodePublicKey(verificationKey(rotated, "overseas", "").PublicKey))
	// 外部身份的公钥也在快照里（外部入口并入网关后要用）；主机网关上它们仍然调不了任何方法。
	require.NoError(t, f.keys.SetIdentity(ctx, "moox-skill", publicKey("moox-skill")))
	require.NotNil(t, verificationKey(f.snapshot(t, "storage"), "", "moox-skill"))
	// 还没签发过身份包的主机没有公钥。
	require.NoError(t, f.db.Exec("UPDATE t_hosts SET c_sign_public_key = '' WHERE c_host_id = 'overseas'").Error)
	require.Nil(t, verificationKey(f.snapshot(t, "storage"), "overseas", ""))
	require.NoError(t, f.keys.SetHostPublicKey(ctx, "overseas", publicKey("host/overseas")))

	// 停用的主机：别的主机快照里它只剩网关这一个调用方（它还要验签取“已停用”的快照），它上面的组件不能再签名。
	require.NoError(t, f.topology.SetHostStatus(ctx, "storage", servicecatalog.Disabled))
	disabled := f.snapshot(t, "storage")
	require.True(t, disabled.Disabled)
	require.Empty(t, disabled.Routes)
	require.NotContains(t, disabled.Directory.Hosts, "storage")
	require.Equal(t, []string{"host-gateway"}, verificationKey(f.snapshot(t, "control"), "storage", "").Callers)

	// 一台主机只能取自己的快照；没有验过的签名主机时拒绝。
	_, err = f.service.PullSnapshot(hostContext("control"), &pb.PullSnapshotReq{HostId: "storage"})
	require.Error(t, err)
	_, err = f.service.PullSnapshot(ctx, &pb.PullSnapshotReq{HostId: "control"})
	require.Error(t, err)
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
	require.Equal(t, before.Hash, during.Hash, "mid-read commits must not mix old routes with new directory/keys")
	after := f.snapshot(t, "storage")
	require.NotEqual(t, before.Hash, after.Hash)
	require.Equal(t, "new.storage.example.test", after.Directory.Hosts["storage"].Address)
	require.Greater(t, len(after.VerificationKeys), len(before.VerificationKeys))
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
	require.Nil(t, verificationKey(pending, "new-host", ""))
	key, err := f.keys.HostPublicKey(ctx, "new-host")
	require.NoError(t, err)
	require.Empty(t, key, "读快照不生成密钥")
	require.NoError(t, f.keys.SetHostPublicKey(ctx, "new-host", publicKey("host/new-host")))
	require.Equal(t, []string{"host-agent", "host-gateway"}, verificationKey(f.snapshot(t, "control"), "new-host", "").Callers)
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
